package send

import (
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/mailbox/internal/render"
)

// RecipientPayload is the machine-readable form of one resolved recipient.
type RecipientPayload struct {
	Address    string `json:"address"`
	Name       string `json:"name"`
	Provenance string `json:"provenance"`
}

// ForwardPayload describes the complete original attached to a forwarded message.
type ForwardPayload struct {
	OriginalBytes int    `json:"originalBytes"`
	Disclosure    string `json:"disclosure"`
}

// SentPayload identifies a message Gmail accepted for delivery.
type SentPayload struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

// EnvelopePayload is the shared JSON and TOON shape for a resolved or sent envelope.
type EnvelopePayload struct {
	Account    string             `json:"account"`
	Mode       string             `json:"mode"`
	ThreadID   string             `json:"threadId,omitempty"`
	Message    string             `json:"message,omitempty"`
	To         []RecipientPayload `json:"to"`
	Cc         []RecipientPayload `json:"cc"`
	Bcc        []RecipientPayload `json:"bcc"`
	Subject    string             `json:"subject"`
	BodyBytes  int                `json:"bodyBytes"`
	InReplyTo  string             `json:"inReplyTo,omitempty"`
	References []string           `json:"references,omitempty"`
	Forward    *ForwardPayload    `json:"forward,omitempty"`
	Sendable   bool               `json:"sendable"`
	Sent       *SentPayload       `json:"sent,omitempty"`
	Scope      string             `json:"scope,omitempty"`
	Warning    string             `json:"warning,omitempty"`
}

// RefusalPayload is the shared JSON and TOON shape for a send refusal.
type RefusalPayload struct {
	Error struct {
		Code    string             `json:"code"`
		Rule    string             `json:"rule"`
		Account string             `json:"account"`
		Message string             `json:"message"`
		ReplyTo []RecipientPayload `json:"replyTo,omitempty"`
		From    []RecipientPayload `json:"from,omitempty"`
	} `json:"error"`
}

// RuleDocs returns the stable refusal rule descriptions used by CLI help and
// generated agent documentation.
func RuleDocs() []struct{ Rule, Code, Doc string } {
	return []struct{ Rule, Code, Doc string }{
		{Rule: "R1", Code: "empty_recipients", Doc: "No recipients remain after resolution."},
		{Rule: "R2", Code: "self_only_recipients", Doc: "A reply's recipients contain only the account's primary address after self-subtraction."},
		{Rule: "R3", Code: "invalid_address", Doc: "A recipient does not parse as an email address."},
		{Rule: "R4", Code: "header_injection", Doc: "A subject or recipient contains CR or LF."},
		{Rule: "R5", Code: "empty_body", Doc: "The message body is empty."},
		{Rule: "R6", Code: "needs_explicit_recipient", Doc: "Reply-To differs from From; provide --to or --cc."},
	}
}

// VisibleOneLine makes layout-relevant whitespace visible, then removes terminal
// control sequences from untrusted text.
func VisibleOneLine(s string) string {
	s = strings.NewReplacer("\r", "␍", "\n", "␊", "\t", "␉").Replace(s)
	return render.SanitizeTerminal(s)
}

// RenderText writes the human-readable envelope preview.
func RenderText(w io.Writer, account string, env *Envelope, forwardOriginalBytes int) {
	fmt.Fprintf(w, "account: %s\n", VisibleOneLine(account))
	fmt.Fprintf(w, "mode: %s\n", env.Mode)
	if env.ThreadID != "" {
		fmt.Fprintf(w, "thread: %s\n", VisibleOneLine(env.ThreadID))
	}
	if env.TargetMessageID != "" {
		fmt.Fprintf(w, "message: %s\n", VisibleOneLine(env.TargetMessageID))
	}
	renderRecipientRows(w, "to", env.To)
	renderRecipientRows(w, "cc", env.Cc)
	renderRecipientRows(w, "bcc", env.Bcc)
	fmt.Fprintf(w, "subject: %s\n", VisibleOneLine(env.Subject))
	fmt.Fprintf(w, "body: %d bytes\n", len([]byte(env.Body)))
	if env.InReplyTo != "" {
		fmt.Fprintf(w, "in-reply-to: %s\n", VisibleOneLine(env.InReplyTo))
	}
	if len(env.References) > 0 {
		fmt.Fprintf(w, "references: %s\n", VisibleOneLine(strings.Join(env.References, " ")))
	}
	if env.Mode == ModeForward {
		fmt.Fprintln(w, forwardDisclosure(forwardOriginalBytes))
	}
}

// RenderRefusalText writes a refusal with its candidate recipient sets, when present.
func RenderRefusalText(w io.Writer, r *Refusal) {
	message := VisibleOneLine(r.Message)
	if r.Rule != "" {
		message = strings.TrimSpace(strings.TrimPrefix(message, r.Rule))
		fmt.Fprintf(w, "(%s) %s\n", r.Rule, message)
	} else {
		fmt.Fprintln(w, message)
	}
	renderRecipientRows(w, "reply-to", r.ReplyTo)
	renderRecipientRows(w, "from", r.From)
}

// Payload converts a resolved envelope into its machine-readable representation.
func Payload(account string, env *Envelope, forwardBytes int) EnvelopePayload {
	payload := EnvelopePayload{
		Account:    account,
		Mode:       env.Mode.String(),
		ThreadID:   env.ThreadID,
		Message:    env.TargetMessageID,
		To:         payloadRecipients(env.To),
		Cc:         payloadRecipients(env.Cc),
		Bcc:        payloadRecipients(env.Bcc),
		Subject:    env.Subject,
		BodyBytes:  len([]byte(env.Body)),
		InReplyTo:  env.InReplyTo,
		References: env.References,
		Sendable:   true,
	}
	if env.Mode == ModeForward {
		payload.Forward = &ForwardPayload{
			OriginalBytes: forwardBytes,
			Disclosure:    forwardDisclosure(forwardBytes),
		}
	}
	return payload
}

// RefusalOf converts a send refusal into its machine-readable representation.
func RefusalOf(account string, r *Refusal) RefusalPayload {
	var payload RefusalPayload
	payload.Error.Code = r.Code
	payload.Error.Rule = r.Rule
	payload.Error.Account = account
	payload.Error.Message = r.Message
	payload.Error.ReplyTo = payloadRecipients(r.ReplyTo)
	payload.Error.From = payloadRecipients(r.From)
	return payload
}

// NotInThreadRefusal reports a pinned message that is not a member of its named thread.
func NotInThreadRefusal(messageID, threadID string) *Refusal {
	return &Refusal{
		Code:    "message_not_in_thread",
		Message: fmt.Sprintf("message %s is not in thread %s", messageID, threadID),
	}
}

func renderRecipientRows(w io.Writer, label string, recipients []Recipient) {
	for _, recipient := range recipients {
		name := VisibleOneLine(recipient.Display)
		if name == "" {
			name = "—"
		}
		fmt.Fprintf(w, "%s  %s  %s  (%s)\n", label, recipient.Address, name, recipient.Provenance)
	}
}

func payloadRecipients(recipients []Recipient) []RecipientPayload {
	payloads := make([]RecipientPayload, len(recipients))
	for i, recipient := range recipients {
		payloads[i] = RecipientPayload{
			Address:    recipient.Address,
			Name:       recipient.Display,
			Provenance: string(recipient.Provenance),
		}
	}
	return payloads
}

func forwardDisclosure(originalBytes int) string {
	return fmt.Sprintf("attaches the complete original (%d bytes) — all original headers (possibly incl. Bcc and delivery metadata), bodies, and attachments", originalBytes)
}
