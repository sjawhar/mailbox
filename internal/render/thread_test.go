package render

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestRenderThread(t *testing.T) {
	thread, first, second := renderThreadFixture(t)

	got, err := RenderThread(thread, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "thread-render" {
		t.Fatalf("RenderThread() ID = %q, want thread-render", got.ID)
	}
	if got.Subject != "Quarterly report" {
		t.Fatalf("RenderThread() Subject = %q, want first message subject", got.Subject)
	}
	if len(got.Messages) != 2 || got.Messages[0].ID != first.ID || got.Messages[1].ID != second.ID {
		t.Fatalf("RenderThread() messages = %#v, want oldest-to-newest %q then %q", got.Messages, first.ID, second.ID)
	}
	if got.Messages[0].Date.Location() != time.UTC || !got.Messages[0].Date.Equal(time.UnixMilli(first.InternalDate).UTC()) {
		t.Fatalf("first rendered date = %v, want UTC InternalDate", got.Messages[0].Date)
	}

	wantParticipants := []string{"Reports <reports@corp.example>", "notifications@github.com"}
	if !reflect.DeepEqual(got.Participants, wantParticipants) {
		t.Fatalf("RenderThread() Participants = %#v, want %#v", got.Participants, wantParticipants)
	}

	if len(got.Messages[0].Links) != 1 || len(got.Messages[1].Links) != 1 {
		t.Fatalf("RenderThread() links = %#v, want one link per message", got.Messages)
	}
	if got.Messages[1].Links[0].N != len(got.Messages[0].Links)+1 {
		t.Fatalf("second message link number = %d, want thread-wide continuation after %d first-message links", got.Messages[1].Links[0].N, len(got.Messages[0].Links))
	}
	wantLinks := append(append([]Link(nil), got.Messages[0].Links...), got.Messages[1].Links...)
	if !reflect.DeepEqual(got.AllLinks(), wantLinks) {
		t.Fatalf("AllLinks() = %#v, want %#v", got.AllLinks(), wantLinks)
	}

	if len(got.Messages[0].Attachments) != 1 || got.Messages[0].Attachments[0].N != 1 {
		t.Fatalf("first message attachments = %#v, want thread-wide attachment N 1", got.Messages[0].Attachments)
	}
	assertRenderedThreadJSONShape(t, got)
}

func TestThreadAttachmentsMatchesRenderThread(t *testing.T) {
	thread, _, _ := renderThreadFixture(t)

	rendered, err := RenderThread(thread, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ThreadAttachments(thread)
	if err != nil {
		t.Fatal(err)
	}

	var want []Attachment
	for _, message := range rendered.Messages {
		want = append(want, message.Attachments...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ThreadAttachments() = %#v, want RenderThread attachment numbering %#v", got, want)
	}
}

func TestRenderedThreadMarkdownFormat(t *testing.T) {
	rt := &RenderedThread{
		Subject: "Status",
		Messages: []RenderedMessage{
			{
				From:     "Alice <alice@example.com>",
				To:       "Bob <bob@example.com>",
				Date:     time.Date(2026, time.August, 27, 1, 2, 0, 0, time.UTC),
				Markdown: "First body\n",
				Attachments: []Attachment{
					{N: 1, Filename: "report.pdf", MimeType: "application/pdf", Size: 10240},
				},
			},
			{
				From:     "Bob <bob@example.com>",
				To:       "Alice <alice@example.com>",
				Date:     time.Date(2026, time.August, 27, 2, 3, 0, 0, time.UTC),
				Markdown: "Second body\n",
			},
		},
	}

	want := "# Status\n\n" +
		"## Alice <alice@example.com> → Bob <bob@example.com>, 2026-08-27 01:02 UTC\n\n" +
		"First body\n\n" +
		"Attachments: [1] report.pdf (application/pdf, 10.0 KB)\n\n" +
		"---\n\n" +
		"## Bob <bob@example.com> → Alice <alice@example.com>, 2026-08-27 02:03 UTC\n\n" +
		"Second body\n"
	if got := rt.Markdown(); got != want {
		t.Fatalf("Markdown() =\n%s\nwant:\n%s", got, want)
	}
}

func TestTerminalLinkTableElidesOnlyGeneratedDefinitions(t *testing.T) {
	fullURL := "https://example.test/private/path?email_token=secret"
	longURL := "https://example.test/" + strings.Repeat("directory/", 20) + "?email_token=secret"
	literal := "Literal source text: [1]: " + fullURL
	thread := &RenderedThread{
		Messages: []RenderedMessage{{
			Markdown: literal + "\n\n[1]: " + fullURL + "\n[2]: " + longURL + "\n",
			Links: []Link{
				{N: 1, URL: fullURL},
				{N: 2, URL: longURL},
			},
		}},
	}

	terminal := thread.Markdown()
	if !strings.Contains(terminal, literal) {
		t.Fatalf("terminal text = %q, want literal body URL preserved", terminal)
	}
	if strings.Contains(terminal, "\n[1]: "+fullURL+"\n") {
		t.Fatalf("terminal link table leaked query parameters: %q", terminal)
	}
	if !strings.Contains(terminal, "https://example.test/private/path…") {
		t.Fatalf("terminal link table = %q, want query-elided URL", terminal)
	}
	for _, line := range strings.Split(terminal, "\n") {
		if strings.HasPrefix(line, "[2]: ") && len(line) > 110 {
			t.Fatalf("terminal link table line = %q, want bounded display length", line)
		}
	}

	encoded, err := json.Marshal(thread)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), fullURL) || !strings.Contains(string(encoded), longURL) {
		t.Fatalf("JSON = %q, want full link URLs", encoded)
	}
}

func renderThreadFixture(t *testing.T) (*gmail.Thread, *gmail.Message, *gmail.Message) {
	t.Helper()

	first := loadFixture(t, "cid_inline.json")
	first.ThreadID = "thread-render"
	first.InternalDate = 1_787_655_600_000
	first.Payload.Parts[0].Body.Data = base64.RawURLEncoding.EncodeToString([]byte(`<p>First <a href="https://example.com/first">link</a></p><img alt="Company logo" src="cid:logo@corp">`))

	second := loadFixture(t, "github_notification.json")
	second.ThreadID = "thread-render"
	second.InternalDate = first.InternalDate + 1_000

	return &gmail.Thread{ID: "thread-render", Messages: []*gmail.Message{second, first}}, first, second
}

func assertRenderedThreadJSONShape(t *testing.T, thread *RenderedThread) {
	t.Helper()

	payload, err := json.Marshal(thread)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, root, []string{"id", "subject", "participants", "messages"})

	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("JSON messages = %s, want two messages", root["messages"])
	}
	for _, message := range messages {
		assertExactJSONKeys(t, message, []string{"id", "from", "to", "date", "markdown", "links", "attachments"})
	}

	var attachments []map[string]json.RawMessage
	if err := json.Unmarshal(messages[0]["attachments"], &attachments); err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("JSON attachments = %s, want one attachment", messages[0]["attachments"])
	}
	assertExactJSONKeys(t, attachments[0], []string{"n", "filename", "mime", "size"})
}

func assertExactJSONKeys(t *testing.T, value map[string]json.RawMessage, want []string) {
	t.Helper()

	if len(value) != len(want) {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("JSON object = %s, want exactly keys %v", encoded, want)
	}
	for _, key := range want {
		if _, ok := value[key]; !ok {
			t.Fatalf("JSON object missing key %q", key)
		}
	}
}
