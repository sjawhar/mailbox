package render

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestOriginalHTML(t *testing.T) {
	older := loadFixture(t, "alternative_human.json")
	older.InternalDate = 1_000
	newer := loadFixture(t, "github_notification.json")
	newer.InternalDate = 2_000

	msgID, html, err := OriginalHTML(&gmail.Thread{ID: "thread-open", Messages: []*gmail.Message{older, newer}})
	if err != nil {
		t.Fatal(err)
	}
	if msgID != newer.ID {
		t.Fatalf("OriginalHTML() message ID = %q, want newest HTML message %q", msgID, newer.ID)
	}
	if !strings.Contains(html, "Pull request #5 needs your review.") || !strings.Contains(html, "display:none") {
		t.Fatalf("OriginalHTML() = %q, want raw newest HTML without cleaning", html)
	}
}

func TestOriginalHTMLWithoutHTML(t *testing.T) {
	textOnly := &gmail.Message{
		ID:           "text-only",
		InternalDate: 1_000,
		Payload: &gmail.MessagePart{
			MimeType: "text/plain",
			Body:     &gmail.PartBody{Data: base64.RawURLEncoding.EncodeToString([]byte("plain text"))},
		},
	}

	_, _, err := OriginalHTML(&gmail.Thread{Messages: []*gmail.Message{textOnly}})
	if err == nil || err.Error() != "thread has no HTML part to open — use 'mailbox read'" {
		t.Fatalf("OriginalHTML() error = %v, want pinned no-HTML error", err)
	}
}

func TestInlineCIDs(t *testing.T) {
	tests := []struct {
		name  string
		msg   func(*testing.T) *gmail.Message
		html  string
		fetch AttachmentFetcher
		want  string
		err   error
	}{
		{
			name: "inlines body data using standard base64",
			msg: func(t *testing.T) *gmail.Message {
				t.Helper()
				return loadFixture(t, "cid_inline.json")
			},
			html: `<img src="cid:logo@corp">`,
			want: `data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==`,
		},
		{
			name: "fetches attachment ID data",
			msg: func(t *testing.T) *gmail.Message {
				t.Helper()
				msg := loadFixture(t, "cid_inline.json")
				part := msg.Payload.Parts[1]
				part.Body.Data = ""
				part.Body.AttachmentID = "att-1"
				return msg
			},
			html: `<img src="cid:logo@corp">`,
			fetch: func(ctx context.Context, messageID, attachmentID string) ([]byte, error) {
				if messageID != "msg-cid" || attachmentID != "att-1" {
					return nil, errors.New("unexpected attachment request")
				}
				return []byte("fetched bytes"), nil
			},
			want: `data:image/png;base64,ZmV0Y2hlZCBieXRlcw==`,
		},
		{
			name: "leaves unknown content ID unchanged",
			msg: func(t *testing.T) *gmail.Message {
				t.Helper()
				return loadFixture(t, "cid_inline.json")
			},
			html: `<img src="cid:missing@corp">`,
			fetch: func(context.Context, string, string) ([]byte, error) {
				return nil, errors.New("fetch must not be called for unknown cid")
			},
			want: `cid:missing@corp`,
		},
		{
			name: "propagates attachment fetch error",
			msg: func(t *testing.T) *gmail.Message {
				t.Helper()
				msg := loadFixture(t, "cid_inline.json")
				part := msg.Payload.Parts[1]
				part.Body.Data = ""
				part.Body.AttachmentID = "att-1"
				return msg
			},
			html: `<img src="cid:logo@corp">`,
			fetch: func(context.Context, string, string) ([]byte, error) {
				return nil, errFetch
			},
			err: errFetch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InlineCIDs(context.Background(), tt.html, tt.msg(t), tt.fetch)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("InlineCIDs() error = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("InlineCIDs() = %q, want %q", got, tt.want)
			}
		})
	}
}

var errFetch = errors.New("fetch failed")

func TestBrowserSafeHTMLRemovesActiveContentAndKeepsDataImages(t *testing.T) {
	input := `<!doctype html><html><head><meta http-equiv="refresh" content="0;url=https://tracker.example"></head><body onload="steal()"><script>steal()</script><iframe src="https://evil.example"></iframe><object data="https://evil.example"></object><embed src="https://evil.example"><form action="https://evil.example"><input name="x"></form><a href="https://evil.example" onclick="steal()">safe link</a><img src="https://tracker.example/pixel" onerror="steal()"><img src="data:image/png;base64,aGVsbG8=" alt="inline"></body></html>`

	output, err := BrowserSafeHTML(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"<script",
		"steal()",
		"<iframe",
		"<object",
		"<embed",
		"<form",
		"<input",
		"refresh",
		"onload=",
		"onclick=",
		"onerror=",
		"https://evil.example",
		"https://tracker.example",
	} {
		if strings.Contains(output, forbidden) {
			t.Errorf("BrowserSafeHTML() retained %q in %q", forbidden, output)
		}
	}
	if !strings.Contains(output, `http-equiv="Content-Security-Policy"`) {
		t.Fatalf("BrowserSafeHTML() = %q, want CSP", output)
	}
	if !strings.Contains(output, `default-src &#39;none&#39;; img-src data:; style-src &#39;unsafe-inline&#39;`) {
		t.Fatalf("BrowserSafeHTML() = %q, want restrictive CSP", output)
	}
	if !strings.Contains(output, `src="data:image/png;base64,aGVsbG8="`) {
		t.Fatalf("BrowserSafeHTML() = %q, want inlined CID data image retained", output)
	}
	if !strings.Contains(output, "safe link") {
		t.Fatalf("BrowserSafeHTML() = %q, want safe link text retained", output)
	}
}

func TestWriteHTMLBackstopWritesSanitizedCIDDocument(t *testing.T) {
	message := loadFixture(t, "cid_inline.json")
	message.Payload.Parts[0].Body.Data = base64.RawURLEncoding.EncodeToString([]byte(`<p onclick="steal()">Quarterly report</p><script>steal()</script><img src="cid:logo@corp">`))

	messageID, path, err := WriteHTMLBackstop(context.Background(), &gmail.Thread{Messages: []*gmail.Message{message}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Errorf("remove browser backstop file: %v", err)
		}
	})
	if messageID != "msg-cid" {
		t.Fatalf("WriteHTMLBackstop() message ID = %q, want msg-cid", messageID)
	}
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(document), "steal()") || strings.Contains(string(document), "onclick=") {
		t.Fatalf("WriteHTMLBackstop() wrote active content: %q", document)
	}
	if !strings.Contains(string(document), "data:image/png;base64") {
		t.Fatalf("WriteHTMLBackstop() = %q, want CID data image", document)
	}
}

func TestOpenURLScrubsCredentials(t *testing.T) {
	directory, capture := t.TempDir(), filepath.Join(t.TempDir(), "environment")
	opener := filepath.Join(directory, "xdg-open")
	script := "#!/bin/sh\nprintf '%s,%s,%s,%s,%s,%s' \"${MAILBOX_TOKEN:-}\" \"${MAILBOX_SECRETS_REEXEC:-}\" \"${GWS_WORK_MAIL_OAUTH:-}\" \"${GWS_PERSONAL_MAIL_OAUTH:-}\" \"${GWS_WORK_READ_OAUTH:-}\" \"${GWS_PERSONAL_SEND_OAUTH:-}\" > " + capture + "\n"
	if err := os.WriteFile(opener, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
	for _, name := range []string{
		"MAILBOX_TOKEN",
		"MAILBOX_SECRETS_REEXEC",
		"GWS_WORK_MAIL_OAUTH",
		"GWS_PERSONAL_MAIL_OAUTH",
		"GWS_WORK_READ_OAUTH",
		"GWS_PERSONAL_SEND_OAUTH",
	} {
		t.Setenv(name, "credential-decoy")
	}

	if err := OpenURL("https://example.test"); err != nil {
		t.Fatal(err)
	}
	environment, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(environment), ",,,,,"; got != want {
		t.Fatalf("OpenURL() child environment = %q, want %q", got, want)
	}
}
