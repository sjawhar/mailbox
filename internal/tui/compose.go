package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/compose"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/paths"
	"github.com/sjawhar/mailbox/internal/send"
)

type composeState struct {
	mode      send.Mode
	threadID  string
	target    *gmail.Message
	draftDir  string
	draftPath string
	to        string
	subject   string
}

type editorDoneMsg struct {
	request asyncRequest
	err     error
}

func (message editorDoneMsg) requestRef() asyncRequest { return message.request }

func newComposeInput(prompt, placeholder string) textinput.Model {
	input := textinput.New()
	input.Prompt = prompt
	input.Placeholder = placeholder
	return input
}

func (m app) startCompose() (tea.Model, tea.Cmd) {
	if m.editorBlocked() {
		return m, nil
	}
	m.composeOrigin = m.view
	m.composeState = composeState{mode: send.ModeCompose}
	m.composeTo.SetValue("")
	m.composeSubject.SetValue("")
	m.view = composeToView
	m.clearStatus()
	return m, m.composeTo.Focus()
}

func (m app) updateComposeToKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.composeTo.Blur()
		m.view = m.composeOrigin
		m.clearStatus()
		return m, nil
	case "enter":
		m.composeState.to = m.composeTo.Value()
		m.composeSubject.SetValue("")
		m.view = composeSubjectView
		return m, m.composeSubject.Focus()
	default:
		var command tea.Cmd
		m.composeTo, command = m.composeTo.Update(message)
		return m, command
	}
}

func (m app) updateComposeSubjectKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.composeSubject.Blur()
		m.view = m.composeOrigin
		m.clearStatus()
		return m, nil
	case "enter":
		if m.editorBlocked() {
			return m, nil
		}
		m.composeState.subject = m.composeSubject.Value()
		envelope, refusal := send.DeriveEnvelope(send.Request{
			Mode:    send.ModeCompose,
			To:      commaSeparatedRecipients(m.composeState.to),
			Subject: m.composeState.subject,
			Self:    m.ctx.self,
		})
		if refusal != nil {
			m.surfaceReplyRefusal(refusal)
			return m, nil
		}
		m.composeTo.Blur()
		m.composeSubject.Blur()
		return m.startEditor(envelope, m.composeState)
	default:
		var command tea.Cmd
		m.composeSubject, command = m.composeSubject.Update(message)
		return m, command
	}
}

func (m app) composeToScreen() string {
	return strings.Join([]string{
		titleStyle.Render("Compose"),
		m.composeTo.View(),
		helpStyle.Render("enter subject · esc cancel"),
		m.statusView(),
	}, "\n")
}

func (m app) composeSubjectScreen() string {
	return strings.Join([]string{
		titleStyle.Render("Compose"),
		m.composeSubject.View(),
		helpStyle.Render("enter editor · esc cancel"),
		m.statusView(),
	}, "\n")
}

func (m app) editorBlocked() bool {
	return m.unlocking || m.pending != nil || m.pendingSend != nil || m.pendingDraft != nil
}

func commaSeparatedRecipients(value string) []string {
	var recipients []string
	for _, raw := range strings.Split(value, ",") {
		if recipient := strings.TrimSpace(raw); recipient != "" {
			recipients = append(recipients, recipient)
		}
	}
	return recipients
}

func (m app) startEditor(envelope *send.Envelope, state composeState) (tea.Model, tea.Cmd) {
	if m.editorBlocked() {
		return m, nil
	}
	argv, err := compose.ResolveEditorCommand(os.LookupEnv)
	if err != nil {
		m.surfaceError(err)
		return m, nil
	}
	cacheDir, err := paths.CacheDir()
	if err != nil {
		m.surfaceError(err)
		return m, nil
	}
	var block strings.Builder
	send.RenderText(&block, m.account, envelope, 0)
	dir, path, err := compose.CreateDraft(filepath.Join(cacheDir, "compose"), block.String(), envelope.Body)
	if err != nil {
		m.surfaceError(err)
		return m, nil
	}
	state.draftDir = dir
	state.draftPath = path
	m.composeState = state
	m.reply = replyModel{threadID: state.threadID, target: state.target, envelope: envelope}
	if state.mode == send.ModeReply {
		m.view = threadView
	} else {
		m.view = listView
	}
	m.clearStatus()
	command := exec.Command(argv[0], append(argv[1:], path)...)
	command.Env = auth.ScrubbedEnviron(m.cfg)
	request := m.beginRequest(composeOperation)
	return m, tea.ExecProcess(command, func(err error) tea.Msg {
		return editorDoneMsg{request: request, err: err}
	})
}

func (m app) finishEditor(message editorDoneMsg) (tea.Model, tea.Cmd) {
	state := m.composeState
	if m.discardAsync(message) {
		if state.draftDir != "" {
			_ = compose.RemoveDraft(state.draftDir)
		}
		return m, nil
	}
	if message.err != nil {
		if err := compose.RemoveDraft(state.draftDir); err != nil {
			m.surfaceError(fmt.Errorf("compose: remove draft: %w", err))
			return m, nil
		}
		m.status = "compose abandoned — editor exited with an error"
		m.statusError = true
		return m, nil
	}

	captured, readErr := os.ReadFile(state.draftPath)
	if err := compose.RemoveDraft(state.draftDir); err != nil {
		m.surfaceError(fmt.Errorf("compose: remove draft: %w", err))
		return m, nil
	}
	if readErr != nil {
		m.surfaceError(fmt.Errorf("compose: read draft: %w", readErr))
		return m, nil
	}
	body, found := compose.ParseDraft(captured)
	if !found {
		m.status = "draft has no scissors line — nothing sent"
		m.statusError = true
		return m, nil
	}

	var envelope *send.Envelope
	var refusal *send.Refusal
	if state.mode == send.ModeReply {
		envelope, refusal = m.resolveReply(state.target, state.threadID, body)
	} else {
		envelope, refusal = send.Resolve(send.Request{
			Mode:    send.ModeCompose,
			To:      commaSeparatedRecipients(state.to),
			Subject: state.subject,
			Body:    body,
			Self:    m.ctx.self,
		})
	}
	if refusal != nil {
		m.surfaceReplyRefusal(refusal)
		return m, nil
	}
	m.reply.envelope = envelope
	m.view = replyConfirmView
	m.clearStatus()
	return m, nil
}
