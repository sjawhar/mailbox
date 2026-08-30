package send

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

func TestBuildMIMEComposeGolden(t *testing.T) {
	env := &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "alice@example.com", Provenance: ProvenanceExplicit}},
		Cc:      []Recipient{{Address: "carol@example.com", Provenance: ProvenanceExplicit}},
		Bcc:     []Recipient{{Address: "blind@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Daily update",
		Body:    "hello\n",
	}
	want := crlf(`To: <alice@example.com>
Cc: <carol@example.com>
Bcc: <blind@example.com>
Subject: Daily update
MIME-Version: 1.0
Content-Type: text/plain; charset=UTF-8
Content-Transfer-Encoding: base64

aGVsbG8K
`)

	got, err := BuildMIME(env, nil, "")
	if err != nil {
		t.Fatalf("BuildMIME() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildMIME() = %q, want %q", got, want)
	}
	assertMessageParses(t, got)
	assertBase64Body(t, got, env.Body)
}

func TestBuildMIMEReplyGolden(t *testing.T) {
	env := &Envelope{
		Mode:       ModeReply,
		To:         []Recipient{{Address: "alice@example.com", Provenance: ProvenanceFrom}},
		Subject:    "Re: Daily update",
		Body:       "hello\n",
		InReplyTo:  "<message@example.com>",
		References: []string{"<root@example.com>", "<message@example.com>"},
	}
	want := crlf(`To: <alice@example.com>
Subject: Re: Daily update
In-Reply-To: <message@example.com>
References: <root@example.com> <message@example.com>
MIME-Version: 1.0
Content-Type: text/plain; charset=UTF-8
Content-Transfer-Encoding: base64

aGVsbG8K
`)

	got, err := BuildMIME(env, nil, "")
	if err != nil {
		t.Fatalf("BuildMIME() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildMIME() = %q, want %q", got, want)
	}
	assertMessageParses(t, got)
	assertBase64Body(t, got, env.Body)
}

func TestBuildMIMEForwardGolden(t *testing.T) {
	const boundary = "mailbox-test-boundary"
	original := []byte("From: source@example.com\r\nBcc: secret@example.com\r\n\r\n--mailbox-test-boundary\r\n")
	env := &Envelope{
		Mode:    ModeForward,
		To:      []Recipient{{Address: "dest@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Fwd: Report",
		Body:    "hello\n",
	}
	want := crlf(`To: <dest@example.com>
Subject: Fwd: Report
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="mailbox-test-boundary"

--mailbox-test-boundary
Content-Transfer-Encoding: base64
Content-Type: text/plain; charset=UTF-8

aGVsbG8K
--mailbox-test-boundary
Content-Disposition: attachment; filename="original.eml"
Content-Transfer-Encoding: base64
Content-Type: message/rfc822

RnJvbTogc291cmNlQGV4YW1wbGUuY29tDQpCY2M6IHNlY3JldEBleGFtcGxlLmNvbQ0KDQotLW1h
aWxib3gtdGVzdC1ib3VuZGFyeQ0K
--mailbox-test-boundary--
`)

	got, err := BuildMIME(env, original, boundary)
	if err != nil {
		t.Fatalf("BuildMIME() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildMIME() = %q, want %q", got, want)
	}

	message := assertMessageParses(t, got)
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" || params["boundary"] != boundary {
		t.Fatalf("outer Content-Type = %q (%v), want multipart/mixed with boundary %q", message.Header.Get("Content-Type"), err, boundary)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	bodyPart, err := parts.NextPart()
	if err != nil {
		t.Fatalf("NextPart(body) error = %v", err)
	}
	if gotBody, err := decodePart(bodyPart); err != nil || gotBody != env.Body {
		t.Fatalf("decoded body = %q, %v; want %q", gotBody, err, env.Body)
	}
	originalPart, err := parts.NextPart()
	if err != nil {
		t.Fatalf("NextPart(original) error = %v", err)
	}
	if originalPart.Header.Get("Content-Type") != "message/rfc822" || originalPart.Header.Get("Content-Disposition") != `attachment; filename="original.eml"` || originalPart.Header.Get("Content-Transfer-Encoding") != "base64" {
		t.Fatalf("original part headers = %#v", originalPart.Header)
	}
	decodedOriginal, err := decodePart(originalPart)
	if err != nil {
		t.Fatalf("decode original part: %v", err)
	}
	if !bytes.Equal([]byte(decodedOriginal), original) {
		t.Fatalf("decoded original = %q, want %q", decodedOriginal, original)
	}
	if part, err := parts.NextPart(); err != io.EOF || part != nil {
		t.Fatalf("NextPart() after original = %v, %v; want EOF", part, err)
	}
	if gotCount := bytes.Count(got, []byte("\r\n--"+boundary)); gotCount != 3 {
		t.Fatalf("boundary line count = %d, want 3 structural boundaries", gotCount)
	}
	if bytes.Contains(got, []byte("\r\n--"+boundary+"\r\nBcc: secret@example.com")) {
		t.Fatal("forwarded boundary-lookalike line escaped its base64 attachment")
	}
}

func TestBuildMIMEDisplayNameEncoding(t *testing.T) {
	env := &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "a@x", Display: "Żółta 🦊, \"quoted\"", Provenance: ProvenanceExplicit}},
		Subject: "Status",
		Body:    "hello",
	}

	got, err := BuildMIME(env, nil, "")
	if err != nil {
		t.Fatalf("BuildMIME() error = %v", err)
	}
	message := assertMessageParses(t, got)
	addresses, err := mail.ParseAddressList(message.Header.Get("To"))
	if err != nil {
		t.Fatalf("ParseAddressList(To) error = %v", err)
	}
	want := &mail.Address{Name: "Żółta 🦊, \"quoted\"", Address: "a@x"}
	if len(addresses) != 1 || *addresses[0] != *want {
		t.Fatalf("ParseAddressList(To) = %#v, want %#v", addresses, want)
	}
}

func TestBuildMIMERefusesControlBytesInHeaderValues(t *testing.T) {
	_, err := BuildMIME(&Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "a@x", Provenance: ProvenanceExplicit}},
		Subject: "a\r\nX: y",
		Body:    "hello",
	}, nil, "")
	if err == nil {
		t.Fatal("BuildMIME() error = nil, want control-byte refusal")
	}
}

func assertMessageParses(t *testing.T, data []byte) *mail.Message {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	return message
}

func assertBase64Body(t *testing.T, data []byte, want string) {
	t.Helper()
	message := assertMessageParses(t, data)
	body, err := decodePart(message.Body)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body != want {
		t.Fatalf("decoded body = %q, want %q", body, want)
	}
}

func decodePart(part io.Reader) (string, error) {
	encoded, err := io.ReadAll(part)
	if err != nil {
		return "", err
	}
	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(encoded)))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func crlf(value string) []byte {
	return []byte(strings.ReplaceAll(value, "\n", "\r\n"))
}
