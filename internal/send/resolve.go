package send

import (
	"net/mail"
	"strings"
)

type Mode uint8

const (
	ModeCompose Mode = iota
	ModeReply
	ModeForward
)

func (m Mode) String() string {
	switch m {
	case ModeCompose:
		return "compose"
	case ModeReply:
		return "reply"
	case ModeForward:
		return "forward"
	default:
		return ""
	}
}

type Provenance string

const (
	ProvenanceReplyTo  Provenance = "Reply-To"
	ProvenanceFrom     Provenance = "From"
	ProvenanceTo       Provenance = "To"
	ProvenanceCC       Provenance = "CC"
	ProvenanceExplicit Provenance = "explicit"
)

type Recipient struct {
	Address    string
	Display    string
	Provenance Provenance
}

type Refusal struct {
	Rule    string
	Code    string
	Message string
	ReplyTo []Recipient
	From    []Recipient
}

func (r *Refusal) Error() string {
	return r.Message
}

// TargetHeaders are the pinned message's decoded, unfolded header values.
type TargetHeaders struct {
	From, ReplyTo, To, Cc, Subject, MessageID, References string
}

type Request struct {
	Mode        Mode
	To, Cc, Bcc []string
	Subject     string
	Body        string
	Self        string
	Target      *TargetHeaders
}

type Envelope struct {
	Mode            Mode
	To, Cc, Bcc     []Recipient
	Subject         string
	Body            string
	InReplyTo       string
	References      []string
	ThreadID        string
	TargetMessageID string
}

// Resolve resolves message recipients and reply metadata without I/O.
func Resolve(req Request) (*Envelope, *Refusal) {
	if hasHeaderInjection(req) {
		return nil, refusal("R4", "header_injection", "R4 header_injection: header values must not contain CR or LF")
	}
	if hasUnsafeDerivedSubject(req) {
		return nil, refusal("R4", "header_injection", "R4 header_injection: original message subject contains control bytes")
	}

	if hasInvalidExplicitAddress(req) {
		return nil, refusal("R3", "invalid_address", "R3 invalid_address: recipient address is invalid")
	}

	if strings.TrimSpace(req.Body) == "" {
		return nil, refusal("R5", "empty_body", "R5 empty_body: message body is empty")
	}

	env := &Envelope{
		Mode: req.Mode,
		Body: req.Body,
	}

	switch req.Mode {
	case ModeCompose:
		env.To = parseExplicit(req.To)
		env.Cc = parseExplicit(req.Cc)
		env.Bcc = parseExplicit(req.Bcc)
		env.Subject = req.Subject
	case ModeForward:
		env.To = parseExplicit(req.To)
		env.Cc = parseExplicit(req.Cc)
		env.Bcc = parseExplicit(req.Bcc)
		env.Subject = "Fwd: " + stripLeadingToken(target(req).Subject, "Fwd:")
	case ModeReply:
		derived := len(req.To) == 0 && len(req.Cc) == 0
		if !derived {
			env.To = parseExplicit(req.To)
			env.Cc = parseExplicit(req.Cc)
		} else {
			replyTo := parseHeader(target(req).ReplyTo, ProvenanceReplyTo)
			from := parseHeader(target(req).From, ProvenanceFrom)
			if len(replyTo) > 0 && !sameRecipientSet(replyTo, from) {
				return nil, &Refusal{
					Rule:    "R6",
					Code:    "needs_explicit_recipient",
					Message: "R6 needs_explicit_recipient: Reply-To differs from From; provide --to or --cc",
					ReplyTo: replyTo,
					From:    from,
				}
			}

			if len(replyTo) > 0 {
				env.To = replyTo
			} else {
				env.To = from
			}
			env.Cc = deriveCC(env.To,
				parseHeader(target(req).To, ProvenanceTo),
				parseHeader(target(req).Cc, ProvenanceCC),
			)
		}
		env.Bcc = parseExplicit(req.Bcc)
		env.To = subtractSelf(env.To, req.Self)
		env.Cc = subtractSelf(env.Cc, req.Self)
		env.Bcc = subtractSelf(env.Bcc, req.Self)
		if derived && len(env.To) == 0 {
			// Replying to your own message: the Reply-To/From set was Self and
			// vanished under subtraction. Promote the original To recipients so
			// a follow-up addresses them directly instead of sending CC-only
			// mail; original CC stays CC. (Spec §3, live-smoke amendment.)
			var to, cc []Recipient
			for _, recipient := range env.Cc {
				if recipient.Provenance == ProvenanceTo {
					to = append(to, recipient)
				} else {
					cc = append(cc, recipient)
				}
			}
			if len(to) > 0 {
				env.To, env.Cc = to, cc
			}
		}
		env.Subject = "Re: " + stripLeadingToken(target(req).Subject, "Re:")
		if messageID := target(req).MessageID; ValidMsgID(messageID) {
			env.InReplyTo = messageID
			for _, token := range strings.Fields(target(req).References) {
				if ValidMsgID(token) {
					env.References = append(env.References, token)
				}
			}
			env.References = append(env.References, messageID)
		}
	}
	if req.Mode == ModeReply && noRecipients(env) {
		return nil, refusal("R2", "self_only_recipients", "R2 self_only_recipients: reply recipients contain only Self")
	}
	if noRecipients(env) {
		return nil, refusal("R1", "empty_recipients", "R1 empty_recipients: no recipients resolved")
	}

	return env, nil
}

func noRecipients(env *Envelope) bool {
	return len(env.To) == 0 && len(env.Cc) == 0 && len(env.Bcc) == 0
}

func refusal(rule, code, message string) *Refusal {
	return &Refusal{Rule: rule, Code: code, Message: message}
}

func hasInvalidExplicitAddress(req Request) bool {
	for _, recipients := range [][]string{req.To, req.Cc, req.Bcc} {
		for _, raw := range recipients {
			if _, err := mail.ParseAddress(raw); err != nil {
				return true
			}
		}
	}
	return false
}

func hasHeaderInjection(req Request) bool {
	if strings.ContainsAny(req.Subject, "\r\n") {
		return true
	}
	for _, recipients := range [][]string{req.To, req.Cc, req.Bcc} {
		for _, raw := range recipients {
			if strings.ContainsAny(raw, "\r\n") {
				return true
			}
		}
	}
	return false
}

func hasUnsafeDerivedSubject(req Request) bool {
	if req.Mode == ModeCompose {
		return false
	}
	subject := target(req).Subject
	for i := range len(subject) {
		if subject[i] < 0x20 || subject[i] == 0x7f {
			return true
		}
	}
	return false
}

func target(req Request) TargetHeaders {
	if req.Target == nil {
		return TargetHeaders{}
	}
	return *req.Target
}

func parseExplicit(values []string) []Recipient {
	out := make([]Recipient, 0, len(values))
	for _, value := range values {
		address, err := mail.ParseAddress(value)
		if err != nil {
			continue
		}
		out = append(out, recipient(address, ProvenanceExplicit))
	}
	return out
}

func parseHeader(value string, provenance Provenance) []Recipient {
	addresses, err := mail.ParseAddressList(value)
	if err != nil {
		return nil
	}
	out := make([]Recipient, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, recipient(address, provenance))
	}
	return dedupe(out, nil)
}

func recipient(address *mail.Address, provenance Provenance) Recipient {
	return Recipient{
		Address:    address.Address,
		Display:    address.Name,
		Provenance: provenance,
	}
}

func deriveCC(to, originalTo, originalCC []Recipient) []Recipient {
	seen := make(map[string]struct{}, len(to)+len(originalTo)+len(originalCC))
	for _, recipient := range to {
		seen[recipientKey(recipient)] = struct{}{}
	}
	cc := dedupe(originalTo, seen)
	for _, recipient := range cc {
		seen[recipientKey(recipient)] = struct{}{}
	}
	return append(cc, dedupe(originalCC, seen)...)
}

func dedupe(recipients []Recipient, seen map[string]struct{}) []Recipient {
	if seen == nil {
		seen = make(map[string]struct{}, len(recipients))
	}
	out := make([]Recipient, 0, len(recipients))
	for _, recipient := range recipients {
		key := recipientKey(recipient)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, recipient)
	}
	return out
}

func sameRecipientSet(left, right []Recipient) bool {
	if len(left) != len(right) {
		return false
	}
	rightSet := make(map[string]struct{}, len(right))
	for _, recipient := range right {
		rightSet[recipientKey(recipient)] = struct{}{}
	}
	for _, recipient := range left {
		if _, ok := rightSet[recipientKey(recipient)]; !ok {
			return false
		}
	}
	return true
}

func subtractSelf(recipients []Recipient, self string) []Recipient {
	out := recipients[:0]
	for _, recipient := range recipients {
		if strings.EqualFold(recipient.Address, self) {
			continue
		}
		out = append(out, recipient)
	}
	return out
}

func recipientKey(recipient Recipient) string {
	return strings.ToLower(recipient.Address)
}

func stripLeadingToken(subject, token string) string {
	for len(subject) >= len(token) && strings.EqualFold(subject[:len(token)], token) {
		subject = strings.TrimLeft(subject[len(token):], " \t")
	}
	return subject
}

// ValidMsgID reports whether token matches the accepted msg-id grammar.
func ValidMsgID(token string) bool {
	if len(token) > 998 || len(token) < 5 || token[0] != '<' || token[len(token)-1] != '>' {
		return false
	}
	inner := token[1 : len(token)-1]
	at := strings.IndexByte(inner, '@')
	if at <= 0 || at == len(inner)-1 {
		return false
	}
	for i := range inner[:at] {
		if !msgIDByte(inner[i]) {
			return false
		}
	}
	for i := at + 1; i < len(inner); i++ {
		if !msgIDByte(inner[i]) {
			return false
		}
	}
	return true
}

func msgIDByte(b byte) bool {
	return (b >= 0x21 && b <= 0x3b) || b == 0x3d || (b >= 0x3f && b <= 0x7e)
}
