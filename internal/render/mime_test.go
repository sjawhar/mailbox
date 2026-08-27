package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/gmail"
)

func loadFixture(t *testing.T, name string) *gmail.Message {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var m gmail.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func TestExtractContent_alternative_human(t *testing.T) {
	content, err := ExtractContent(loadFixture(t, "alternative_human.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.HTML, "Thanks for the update.") {
		t.Fatalf("HTML = %q, want selected HTML body", content.HTML)
	}
	if content.Text != "Hi Alice,\n\nThanks for the update." {
		t.Fatalf("Text = %q", content.Text)
	}

	stripped, err := CleanHTML(content.HTML, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !stripped.QuoteStripped || strings.Contains(stripped.HTML, "Earlier message.") {
		t.Fatalf("default CleanHTML = %#v, want quoted history stripped", stripped)
	}

	full, err := CleanHTML(content.HTML, Options{KeepQuotes: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.QuoteStripped || !strings.Contains(full.HTML, "gmail_quote") {
		t.Fatalf("KeepQuotes CleanHTML = %#v, want quoted history retained", full)
	}
}

func TestExtractContent_github_notification(t *testing.T) {
	content, err := ExtractContent(loadFixture(t, "github_notification.json"))
	if err != nil {
		t.Fatal(err)
	}
	if content.Text != "" {
		t.Fatalf("Text = %q, want no text/plain fallback", content.Text)
	}

	clean, err := CleanHTML(content.HTML, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean.HTML, "preheader") || strings.Contains(clean.HTML, "tracking.gif") {
		t.Fatalf("CleanHTML = %q, want hidden preheader and tracking pixel removed", clean.HTML)
	}
	if !strings.Contains(clean.HTML, `href="https://github.com/o/r/pull/5"`) {
		t.Fatalf("CleanHTML = %q, want GitHub link preserved", clean.HTML)
	}
}

func TestExtractContent_marketing_table(t *testing.T) {
	content, err := ExtractContent(loadFixture(t, "marketing_table.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.HTML, "“Big Sale”") {
		t.Fatalf("HTML = %q, want windows-1252 curly quotes decoded to UTF-8", content.HTML)
	}

	clean, err := CleanHTML(content.HTML, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clean.HTML, "[image: Sale banner]") {
		t.Fatalf("CleanHTML = %q, want alt-bearing image replacement", clean.HTML)
	}
	if strings.Contains(clean.HTML, "spacer.jpg") {
		t.Fatalf("CleanHTML = %q, want alt-less image removed", clean.HTML)
	}
}

func TestExtractContent_cid_inline(t *testing.T) {
	msg := loadFixture(t, "cid_inline.json")
	content, err := ExtractContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	if content.InlineParts["logo@corp"] == nil {
		t.Fatalf("InlineParts = %#v, want logo@corp", content.InlineParts)
	}
	if len(content.Attachments) != 1 {
		t.Fatalf("Attachments = %#v, want exactly one PDF", content.Attachments)
	}
	attachment := content.Attachments[0]
	if attachment.Filename != "report.pdf" || attachment.MimeType != "application/pdf" || attachment.Size != 10240 || attachment.MessageID != msg.ID || attachment.AttachmentID != "att-1" {
		t.Fatalf("Attachment = %#v, want report.pdf metadata and transport IDs", attachment)
	}
}

func TestExtractContent_nested_quotes(t *testing.T) {
	content, err := ExtractContent(loadFixture(t, "nested_quotes.json"))
	if err != nil {
		t.Fatal(err)
	}
	clean, err := CleanHTML(content.HTML, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !clean.QuoteStripped || strings.Contains(clean.HTML, "First quoted reply.") || strings.Contains(clean.HTML, "Original quoted message.") {
		t.Fatalf("CleanHTML = %#v, want both nested quote levels removed", clean)
	}
}

func TestExtractContent_TextOnly(t *testing.T) {
	msg := &gmail.Message{
		Payload: &gmail.MessagePart{
			MimeType: "text/plain",
			Body:     &gmail.PartBody{Data: "SGVsbG8gdGV4dC4"},
		},
	}

	content, err := ExtractContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	if content.HTML != "" || content.Text != "Hello text." {
		t.Fatalf("content = %#v, want text/plain fallback only", content)
	}
}

func TestExtractContent_FirstPlainPartWinsWhenEmpty(t *testing.T) {
	msg := &gmail.Message{
		Payload: &gmail.MessagePart{
			MimeType: "multipart/alternative",
			Parts: []*gmail.MessagePart{
				{MimeType: "text/plain", Body: &gmail.PartBody{}},
				{MimeType: "text/plain", Body: &gmail.PartBody{Data: "bGF0ZXIgcGxhaW4"}},
			},
		},
	}

	content, err := ExtractContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	if content.Text != "" {
		t.Fatalf("Text = %q, want first empty text/plain part", content.Text)
	}
}

func TestExtractContent_UnknownCharsetWithoutBodyData(t *testing.T) {
	msg := &gmail.Message{
		Payload: &gmail.MessagePart{
			PartID:   "plain-unknown",
			MimeType: "text/plain",
			Headers:  []gmail.Header{{Name: "Content-Type", Value: "text/plain; charset=unknown-charset"}},
			Body:     &gmail.PartBody{},
		},
	}

	_, err := ExtractContent(msg)
	if err == nil {
		t.Fatal("ExtractContent returned nil error for an unknown charset")
	}
	if !strings.Contains(err.Error(), "plain-unknown") || !strings.Contains(err.Error(), "unknown-charset") {
		t.Fatalf("error = %q, want MIME part and charset", err)
	}
}

func TestExtractContent_LargestNestedHTMLPartWins(t *testing.T) {
	msg := &gmail.Message{
		Payload: &gmail.MessagePart{
			MimeType: "multipart/mixed",
			Parts: []*gmail.MessagePart{
				{
					MimeType: "multipart/alternative",
					Parts: []*gmail.MessagePart{
						{MimeType: "text/html", Body: &gmail.PartBody{Data: "PHA-c21hbGwgSFRNTDwvcD4"}},
					},
				},
				{
					MimeType: "multipart/related",
					Parts: []*gmail.MessagePart{
						{MimeType: "text/html", Body: &gmail.PartBody{Data: "PGRpdj5sYXJnZXIgSFRNTCBib2R5IHNlbGVjdGVkPC9kaXY-"}},
					},
				},
			},
		},
	}

	content, err := ExtractContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	if content.HTML != "<div>larger HTML body selected</div>" {
		t.Fatalf("HTML = %q, want largest nested HTML body", content.HTML)
	}
}

func TestAttachmentJSONShape(t *testing.T) {
	payload, err := json.Marshal(Attachment{
		N:            3,
		Filename:     "report.pdf",
		MimeType:     "application/pdf",
		Size:         10240,
		MessageID:    "message-1",
		AttachmentID: "attachment-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("JSON = %s, want exactly n, filename, mime, size", payload)
	}
	for _, key := range []string{"n", "filename", "mime", "size"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("JSON = %s, missing key %q", payload, key)
		}
	}
	for _, key := range []string{"messageId", "attachmentId"} {
		if _, ok := got[key]; ok {
			t.Fatalf("JSON = %s, must not expose %q", payload, key)
		}
	}
}
