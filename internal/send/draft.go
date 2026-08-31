package send

import "strings"

// DraftThreading carries a resumed draft's raw threading fields. Values are
// untrusted: they came from a Gmail draft that any client may have written.
type DraftThreading struct {
	ThreadID   string
	InReplyTo  string
	References string
}

// ResolveDraft validates a reconstructed draft request through the full
// resolver. Recipients, subject, and body are explicit compose-rule fields
// (R1, R3, R4, R5); threading fields are CR/LF-checked, msg-id-filtered, and
// carried without derivation. A carried In-Reply-To yields a reply-mode
// envelope so BuildMIME emits the threading headers. Raw draft MIME headers
// are never copied into the outbound message.
func ResolveDraft(req Request, threading DraftThreading) (*Envelope, *Refusal) {
	for _, value := range []string{threading.ThreadID, threading.InReplyTo, threading.References} {
		if strings.ContainsAny(value, "\r\n") {
			return nil, refusal("R4", "header_injection", "R4 header_injection: draft threading fields must not contain CR or LF")
		}
	}
	req.Mode = ModeCompose
	req.Target = nil
	env, r := resolveEnvelope(req, true)
	if r != nil {
		return nil, r
	}
	env.ThreadID = threading.ThreadID
	if ValidMsgID(threading.InReplyTo) {
		env.Mode = ModeReply
		env.InReplyTo = threading.InReplyTo
		for _, token := range strings.Fields(threading.References) {
			if ValidMsgID(token) {
				env.References = append(env.References, token)
			}
		}
	}
	return env, nil
}
