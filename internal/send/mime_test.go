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

func TestBuildMIMEComposeAlternativeGolden(t *testing.T) {
	const boundary = "mailbox-test-boundary"
	env := &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "alice@example.com", Provenance: ProvenanceExplicit}},
		Cc:      []Recipient{{Address: "carol@example.com", Provenance: ProvenanceExplicit}},
		Bcc:     []Recipient{{Address: "blind@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Daily update",
		Body:    "hello **there**\n",
	}
	got, err := BuildMIME(env, nil, boundary)
	if err != nil {
		t.Fatal(err)
	}
	want := crlf(`To: <alice@example.com>
Cc: <carol@example.com>
Bcc: <blind@example.com>
Subject: Daily update
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="mailbox-test-boundary"

--mailbox-test-boundary
Content-Transfer-Encoding: base64
Content-Type: text/plain; charset=UTF-8

aGVsbG8gKip0aGVyZSoqCg==
--mailbox-test-boundary
Content-Transfer-Encoding: base64
Content-Type: text/html; charset=UTF-8

PHA+aGVsbG8gPHN0cm9uZz50aGVyZTwvc3Ryb25nPjwvcD4K
--mailbox-test-boundary--
`)
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildMIME() =\n%q\nwant:\n%q", got, want)
	}
	assertMessageParses(t, got)
	plain, html := decodeAlternativeLeaves(t, got)
	if plain != env.Body || html != "<p>hello <strong>there</strong></p>\n" {
		t.Fatalf("decoded leaves = (%q, %q), want (%q, %q)", plain, html, env.Body, "<p>hello <strong>there</strong></p>\n")
	}
}

func TestBuildMIMEReplyAlternativeGolden(t *testing.T) {
	const boundary = "mailbox-test-boundary"
	env := &Envelope{
		Mode:       ModeReply,
		To:         []Recipient{{Address: "alice@example.com", Provenance: ProvenanceFrom}},
		Subject:    "Re: Daily update",
		Body:       "hello\n",
		InReplyTo:  "<message@example.com>",
		References: []string{"<root@example.com>", "<message@example.com>"},
	}
	got, err := BuildMIME(env, nil, boundary)
	if err != nil {
		t.Fatal(err)
	}
	want := crlf(`To: <alice@example.com>
Subject: Re: Daily update
In-Reply-To: <message@example.com>
References: <root@example.com> <message@example.com>
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="mailbox-test-boundary"

--mailbox-test-boundary
Content-Transfer-Encoding: base64
Content-Type: text/plain; charset=UTF-8

aGVsbG8K
--mailbox-test-boundary
Content-Transfer-Encoding: base64
Content-Type: text/html; charset=UTF-8

PHA+aGVsbG88L3A+Cg==
--mailbox-test-boundary--
`)
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildMIME() =\n%q\nwant:\n%q", got, want)
	}
	assertMessageParses(t, got)
}

func TestAlternativePlainLeafByteEqualsInput(t *testing.T) {
	body := "line1\r\nline2 — ümlaut, no trailing newline"
	env := &Envelope{Mode: ModeCompose, To: []Recipient{{Address: "a@b.c"}}, Subject: "s", Body: body}
	got, err := BuildMIME(env, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	plain, html := decodeAlternativeLeaves(t, got)
	if plain != body {
		t.Fatalf("decoded plain leaf = %q, want input byte-for-byte", plain)
	}
	if !strings.Contains(html, "<p>") {
		t.Fatalf("html leaf = %q, want rendered HTML", html)
	}
}

func TestBoundaryShapedBodyLineStaysInert(t *testing.T) {
	const boundary = "mailbox-test-boundary"
	env := &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "a@b.c"}},
		Subject: "s",
		Body:    "before\n--" + boundary + "\nafter\n",
	}
	got, err := BuildMIME(env, nil, boundary)
	if err != nil {
		t.Fatalf("BuildMIME must not fail on boundary-shaped body content: %v", err)
	}
	plain, _ := decodeAlternativeLeaves(t, got)
	if plain != env.Body {
		t.Fatalf("boundary-shaped body line corrupted the part: %q", plain)
	}
	if bytes.Count(got, []byte("\r\n--"+boundary)) != 3 {
		t.Fatalf("boundary appeared raw in the wire body:\n%q", got)
	}
}

func TestBuildMIMEForwardGolden(t *testing.T) {
	const mixedBoundary = "mixed-b"
	const alternativeBoundary = "alt-b"
	original := []byte("From: source@example.com\r\nBcc: secret@example.com\r\n\r\n--mailbox-test-boundary\r\n")
	env := &Envelope{
		Mode:    ModeForward,
		To:      []Recipient{{Address: "dest@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Fwd: Report",
		Body:    "hello\n",
	}
	got, err := buildForwardMIME(env, original, mixedBoundary, alternativeBoundary)
	if err != nil {
		t.Fatal(err)
	}
	want := crlf(`To: <dest@example.com>
Subject: Fwd: Report
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="mixed-b"

--mixed-b
Content-Type: multipart/alternative; boundary="alt-b"

--alt-b
Content-Transfer-Encoding: base64
Content-Type: text/plain; charset=UTF-8

aGVsbG8K
--alt-b
Content-Transfer-Encoding: base64
Content-Type: text/html; charset=UTF-8

PHA+aGVsbG88L3A+Cg==
--alt-b--

--mixed-b
Content-Disposition: attachment; filename="original.eml"
Content-Transfer-Encoding: base64
Content-Type: message/rfc822

RnJvbTogc291cmNlQGV4YW1wbGUuY29tDQpCY2M6IHNlY3JldEBleGFtcGxlLmNvbQ0KDQotLW1h
aWxib3gtdGVzdC1ib3VuZGFyeQ0K
--mixed-b--
`)
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildMIME() =\n%q\nwant:\n%q", got, want)
	}

	message := assertMessageParses(t, got)
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" || params["boundary"] != mixedBoundary {
		t.Fatalf("outer Content-Type = %q (%v), want multipart/mixed with boundary %q", message.Header.Get("Content-Type"), err, mixedBoundary)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	bodyPart, err := parts.NextPart()
	if err != nil {
		t.Fatalf("NextPart(body) error = %v", err)
	}
	bodyType, bodyParams, err := mime.ParseMediaType(bodyPart.Header.Get("Content-Type"))
	if err != nil || bodyType != "multipart/alternative" || bodyParams["boundary"] != alternativeBoundary {
		t.Fatalf("body Content-Type = %q (%v), want multipart/alternative with boundary %q", bodyPart.Header.Get("Content-Type"), err, alternativeBoundary)
	}
	plain, html := decodeAlternativeParts(t, bodyPart, alternativeBoundary)
	if plain != env.Body || html != "<p>hello</p>\n" {
		t.Fatalf("decoded forward leaves = (%q, %q)", plain, html)
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
	if gotCount := bytes.Count(got, []byte("\r\n--"+mixedBoundary)); gotCount != 3 {
		t.Fatalf("outer boundary line count = %d, want 3 structural boundaries", gotCount)
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
		t.Fatalf("ParseAddressList(To)) error = %v", err)
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

func decodeAlternativeLeaves(t *testing.T, data []byte) (string, string) {
	t.Helper()
	message := assertMessageParses(t, data)
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q (%v), want multipart/alternative with a boundary", message.Header.Get("Content-Type"), err)
	}
	return decodeAlternativeParts(t, message.Body, params["boundary"])
}

func decodeAlternativeParts(t *testing.T, body io.Reader, boundary string) (string, string) {
	t.Helper()
	parts := multipart.NewReader(body, boundary)
	wantTypes := []string{"text/plain; charset=UTF-8", "text/html; charset=UTF-8"}
	decoded := make([]string, len(wantTypes))
	for index, wantType := range wantTypes {
		part, err := parts.NextPart()
		if err != nil {
			t.Fatalf("NextPart(%d) error = %v", index, err)
		}
		if gotType := part.Header.Get("Content-Type"); gotType != wantType || part.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Fatalf("part %d headers = %#v, want Content-Type %q and base64", index, part.Header, wantType)
		}
		decoded[index], err = decodePart(part)
		if err != nil {
			t.Fatalf("decode part %d: %v", index, err)
		}
	}
	if part, err := parts.NextPart(); err != io.EOF || part != nil {
		t.Fatalf("NextPart() after alternative leaves = %v, %v; want EOF", part, err)
	}
	return decoded[0], decoded[1]
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
