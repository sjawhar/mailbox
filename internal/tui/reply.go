package tui

import (
	"errors"
	"strings"

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
}

type pendingSend struct {
	mime     []byte
	threadID string
	retried  bool
}

type pendingDraft struct {
	mime     []byte
	threadID string
	retried  bool
}

func (m app) openReply() (tea.Model, tea.Cmd) {
	if m.editorBlocked() {
		return m, nil
	}
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
	envelope, refusal := m.deriveReply(target, m.thread.thread.ID)
	if refusal != nil {
		m.surfaceReplyRefusal(refusal)
		return m, nil
	}
	return m.startEditor(envelope, composeState{
		mode:     send.ModeReply,
		threadID: m.thread.thread.ID,
		target:   target,
	})
}

func (m app) deriveReply(target *gmail.Message, threadID string) (*send.Envelope, *send.Refusal) {
	envelope, refusal := send.DeriveEnvelope(m.replyRequest(target, ""))
	return finishReplyEnvelope(target, threadID, envelope, refusal)
}

func (m app) resolveReply(target *gmail.Message, threadID, body string) (*send.Envelope, *send.Refusal) {
	envelope, refusal := send.Resolve(m.replyRequest(target, body))
	return finishReplyEnvelope(target, threadID, envelope, refusal)
}

func (m app) replyRequest(target *gmail.Message, body string) send.Request {
	return send.Request{
		Mode:   send.ModeReply,
		Body:   body,
		Self:   m.ctx.self,
		Target: replyTargetHeaders(target),
	}
}

func finishReplyEnvelope(target *gmail.Message, threadID string, envelope *send.Envelope, refusal *send.Refusal) (*send.Envelope, *send.Refusal) {
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

func (m *app) surfaceReplyRefusal(refusal *send.Refusal) {
	var output strings.Builder
	send.RenderRefusalText(&output, refusal)
	line, _, _ := strings.Cut(output.String(), "\n")
	m.status = render.SanitizeTerminal(line)
	m.statusError = true
}

func (m app) replyConfirmScreen() string {
	help := "y send · esc back"
	if m.abandonPrompt {
		help = "abandon? d discard · s save to Gmail drafts · e keep editing · esc/enter discard"
	}
	return strings.Join([]string{
		titleStyle.Render("Confirm send"),
		replyEnvelopeText(m.account, m.reply.envelope),
		helpStyle.Render(help),
		m.statusView(),
	}, "\n")
}

func replyEnvelopeText(account string, envelope *send.Envelope) string {
	var output strings.Builder
	send.RenderText(&output, account, envelope, 0)
	return render.SanitizeTerminal(output.String())
}

func (m app) updateReplyConfirmKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		if m.unlocking && m.unlockClass == auth.ClassSend && m.pendingSend != nil {
			m.abandonUnlock()
		}
		if m.abandonPrompt {
			return m.discardComposed()
		}
		m.abandonPrompt = true
		m.clearStatus()
		return m, nil
	case "enter", "d":
		if m.abandonPrompt {
			return m.discardComposed()
		}
		return m, nil
	case "s":
		if !m.abandonPrompt {
			return m, nil
		}
		mime, err := send.BuildMIME(m.reply.envelope, nil, "")
		if err != nil {
			m.surfaceError(err)
			return m, nil
		}
		if refusal := send.OutboundSizeRefusal(mime, m.reply.envelope.Attachments); refusal != nil {
			m.surfaceReplyRefusal(refusal)
			return m, nil
		}
		m.abandonPrompt = false
		m.pendingDraft = &pendingDraft{mime: mime, threadID: m.reply.threadID}
		return m.startClassUnlock(auth.ClassWrite)
	case "e":
		if !m.abandonPrompt {
			return m, nil
		}
		m.abandonPrompt = false
		return m.startEditor(m.reply.envelope, m.composeState)
	case keyConfirmSend:
		if m.abandonPrompt {
			return m, nil
		}
		mime, err := send.BuildMIME(m.reply.envelope, nil, "")
		if err != nil {
			m.surfaceError(err)
			return m, nil
		}
		if refusal := send.OutboundSizeRefusal(mime, m.reply.envelope.Attachments); refusal != nil {
			m.surfaceReplyRefusal(refusal)
			return m, nil
		}
		m.pendingSend = &pendingSend{mime: mime, threadID: m.reply.threadID}
		return m.startClassUnlock(auth.ClassSend)
	default:
		return m, nil
	}
}

func (m app) discardComposed() (tea.Model, tea.Cmd) {
	if m.reply.envelope != nil && m.reply.envelope.Mode == send.ModeCompose {
		m.view = listView
	} else {
		m.view = threadView
	}
	m.reply = replyModel{}
	m.composeState = composeState{}
	m.abandonPrompt = false
	m.clearStatus()
	return m, nil
}
