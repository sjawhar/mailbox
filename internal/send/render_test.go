package send

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderTextAddrSpecColumnSurvivesSpoofedDisplayName(t *testing.T) {
	env := &Envelope{
		Mode: ModeCompose,
		To: []Recipient{{
			Address:    "real@example.com",
			Display:    "innocent@other.com <fake@evil.com>\r\nTo: forged@evil.com",
			Provenance: ProvenanceExplicit,
		}},
		Subject: "Status",
		Body:    "update",
	}

	var output bytes.Buffer
	RenderText(&output, "work", env, 0)

	var recipientLine string
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "to  ") {
			recipientLine = line
		}
		if strings.HasPrefix(line, "To: forged") {
			t.Fatalf("spoofed display name created a forged header line in %q", output.String())
		}
	}
	if !strings.HasPrefix(recipientLine, "to  real@example.com  ") {
		t.Fatalf("recipient line = %q, want fixed addr-spec first", recipientLine)
	}
	if !strings.Contains(recipientLine, "␍␊To: forged@evil.com") {
		t.Fatalf("recipient line = %q, want visible CR/LF", recipientLine)
	}
}

func TestRenderTextProvenanceLabels(t *testing.T) {
	derived := &Envelope{
		Mode: ModeReply,
		To:   []Recipient{{Address: "reply@example.com", Provenance: ProvenanceReplyTo}},
		Cc: []Recipient{
			{Address: "to@example.com", Provenance: ProvenanceTo},
			{Address: "cc@example.com", Provenance: ProvenanceCC},
		},
		Subject: "Re: Status",
		Body:    "update",
	}
	explicit := &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "explicit@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Status",
		Body:    "update",
	}

	var output bytes.Buffer
	RenderText(&output, "work", derived, 0)
	RenderText(&output, "work", explicit, 0)

	for _, label := range []string{"(Reply-To)", "(To)", "(CC)", "(explicit)"} {
		if !strings.Contains(output.String(), label) {
			t.Fatalf("RenderText() = %q, want provenance label %q", output.String(), label)
		}
	}
}

func TestRenderTextForwardDisclosure(t *testing.T) {
	env := &Envelope{
		Mode:    ModeForward,
		To:      []Recipient{{Address: "dest@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Fwd: Status",
		Body:    "FYI",
	}

	var output bytes.Buffer
	RenderText(&output, "work", env, 8437)

	const disclosure = "attaches the complete original (8437 bytes) — all original headers (possibly incl. Bcc and delivery metadata), bodies, and attachments"
	if !strings.Contains(output.String(), disclosure) {
		t.Fatalf("RenderText() = %q, want disclosure %q", output.String(), disclosure)
	}
}

func TestVisibleOneLine(t *testing.T) {
	if got, want := VisibleOneLine("a\r\nb\tc"), "a␍␊b␉c"; got != want {
		t.Fatalf("VisibleOneLine() = %q, want %q", got, want)
	}
	if got, want := VisibleOneLine("safe\x1b[31mname\x1b[0m"), "safename"; got != want {
		t.Fatalf("VisibleOneLine() = %q, want terminal controls removed", got)
	}
}

func TestPayloadEmptySetsAreArrays(t *testing.T) {
	payload := Payload("work", &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "dest@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Status",
		Body:    "update",
	}, 0)

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, field := range []string{`"cc":[]`, `"bcc":[]`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("json.Marshal(Payload()) = %s, want %s", encoded, field)
		}
	}
}

func TestNotInThreadRefusalRendersEverywhere(t *testing.T) {
	refusal := NotInThreadRefusal("m-2", "t-1")

	var text bytes.Buffer
	RenderRefusalText(&text, refusal)
	if strings.Contains(text.String(), "(R") {
		t.Fatalf("RenderRefusalText() = %q, want no rule prefix", text.String())
	}
	for _, id := range []string{"m-2", "t-1"} {
		if !strings.Contains(text.String(), id) {
			t.Fatalf("RenderRefusalText() = %q, want %q", text.String(), id)
		}
	}

	encoded, err := json.Marshal(RefusalOf("work", refusal))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const want = `{"error":{"code":"message_not_in_thread","rule":"","account":"work","message":"message m-2 is not in thread t-1"}}`
	if got := string(encoded); got != want {
		t.Fatalf("json.Marshal(RefusalOf()) = %s, want %s", got, want)
	}
}

func TestRenderTextAttachmentLineAndPayloadMetadata(t *testing.T) {
	env := &Envelope{
		Mode:    ModeCompose,
		To:      []Recipient{{Address: "dest@example.com", Provenance: ProvenanceExplicit}},
		Subject: "Status",
		Body:    "update",
		Attachments: []Attachment{{
			Filename: "report.pdf",
			MIMEType: "application/pdf",
			SHA256:   "abc123",
			Content:  []byte("%PDF"),
		}},
	}

	var text bytes.Buffer
	RenderText(&text, "work", env, 0)
	const wantLine = "attachment: report.pdf (4 bytes, application/pdf) sha256=abc123"
	if !strings.Contains(text.String(), wantLine) {
		t.Fatalf("RenderText() = %q, want %q", text.String(), wantLine)
	}

	encoded, err := json.Marshal(Payload("work", env, 0))
	if err != nil {
		t.Fatalf("json.Marshal(Payload()) error = %v", err)
	}
	for _, field := range []string{
		`"filename":"report.pdf"`,
		`"size":4`,
		`"mime_type":"application/pdf"`,
		`"sha256":"abc123"`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("json.Marshal(Payload()) = %s, want %s", encoded, field)
		}
	}
}
