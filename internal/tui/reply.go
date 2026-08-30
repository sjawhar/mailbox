package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
	"github.com/sjawhar/mailbox/internal/send"
)

var errNoReplyTarget = errors.New("thread has no messages to reply to")

type replyModel struct {
	threadID string
	target   *gmail.Message
	envelope *send.Envelope
	body     textarea.Model
}

type pendingSend struct {
	mime     []byte
	threadID string
	retried  bool
}

func (m app) openReply() (tea.Model, tea.Cmd) {
	if m.thread.thread == nil {
		m.surfaceError(errNoReplyTarget)
		return m, nil
	}
	if m.ctx.self == "" {
		m.loading = true
		request := m.beginRequest(profileOperation)
		return m, m.loadingCmd(getProfileCmd(request))
	}

	target := gmail.LatestMessage(m.thread.thread)
	if target == nil {
		m.surfaceError(errNoReplyTarget)
		return m, nil
	}
	// A draft has no body yet, so use a private non-empty value to derive the
	// read-only recipient fields. Confirmation resolves the actual body.
	envelope, refusal := m.resolveReply(target, m.thread.thread.ID, "draft")
	if refusal != nil {
		m.surfaceReplyRefusal(refusal)
		return m, nil
	}
	envelope.Body = ""

	body := textarea.New()
	body.Prompt = ""
	body.Placeholder = "Reply body"
	body.CharLimit = 0
	m.reply = replyModel{
		threadID: m.thread.thread.ID,
		target:   target,
		envelope: envelope,
		body:     body,
	}
	m.resizeReply()
	m.view = replyView
	m.clearStatus()
	return m, m.reply.body.Focus()
}

func (m app) resolveReply(target *gmail.Message, threadID, body string) (*send.Envelope, *send.Refusal) {
	envelope, refusal := send.Resolve(send.Request{
		Mode:   send.ModeReply,
		Body:   body,
		Self:   m.ctx.self,
		Target: replyTargetHeaders(target),
	})
	if refusal != nil {
		return nil, refusal
	}
	envelope.ThreadID = threadID
	envelope.TargetMessageID = target.ID
	return envelope, nil
}

func replyTargetHeaders(target *gmail.Message) *send.TargetHeaders {
	return &send.TargetHeaders{
		From:       target.Header("From"),
		ReplyTo:    target.Header("Reply-To"),
		To:         target.Header("To"),
		Cc:         target.Header("Cc"),
		Subject:    target.Header("Subject"),
		MessageID:  target.Header("Message-ID"),
		References: target.Header("References"),
	}
}

func (m *app) resizeReply() {
	if m.reply.target == nil {
		return
	}
	m.reply.body.SetWidth(max(20, m.layout.readerWidth))
	m.reply.body.SetHeight(max(3, min(8, m.layout.readerHeight/3)))
}

func (m *app) surfaceReplyRefusal(refusal *send.Refusal) {
	var output strings.Builder
	send.RenderRefusalText(&output, refusal)
	line, _, _ := strings.Cut(output.String(), "\n")
	m.status = render.SanitizeTerminal(line)
	m.statusError = true
}

func (m app) replyScreen() string {
	return strings.Join([]string{
		titleStyle.Render("Reply"),
		replyEnvelopeText(m.account, m.reply.envelope),
		"Body:",
		m.reply.body.View(),
		helpStyle.Render("ctrl+s confirm · esc back"),
		m.statusView(),
	}, "\n")
}

func (m app) replyConfirmScreen() string {
	return strings.Join([]string{
		titleStyle.Render("Confirm send"),
		replyEnvelopeText(m.account, m.reply.envelope),
		helpStyle.Render("y send · esc back"),
		m.statusView(),
	}, "\n")
}

func replyEnvelopeText(account string, envelope *send.Envelope) string {
	var output strings.Builder
	send.RenderText(&output, account, envelope, 0)
	return render.SanitizeTerminal(output.String())
}

func (m app) updateReplyKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.reply = replyModel{}
		m.view = threadView
		m.clearStatus()
		return m, nil
	case keyReplyProceed:
		envelope, refusal := m.resolveReply(m.reply.target, m.reply.threadID, m.reply.body.Value())
		if refusal != nil {
			m.surfaceReplyRefusal(refusal)
			return m, nil
		}
		m.reply.envelope = envelope
		m.view = replyConfirmView
		m.clearStatus()
		return m, nil
	default:
		var command tea.Cmd
		m.reply.body, command = m.reply.body.Update(message)
		return m, command
	}
}

func (m app) updateReplyConfirmKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.view = replyView
		return m, nil
	case keyConfirmSend:
		mime, err := send.BuildMIME(m.reply.envelope, nil, "")
		if err != nil {
			m.surfaceError(err)
			return m, nil
		}
		m.pendingSend = &pendingSend{mime: mime, threadID: m.reply.threadID}
		return m.startClassUnlock(auth.ClassSend, sendOperation)
	default:
		return m, nil
	}
}
