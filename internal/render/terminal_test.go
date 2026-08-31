package render

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeTerminalRemovesControlSequences(t *testing.T) {
	input := "subject\x1b[31m red\x1b[0m\x1b]52;c;clipboard\a\x1bPprivate\x1b\\\x9b31mansi\x9c\x00\r\tindent\nbody"
	got := SanitizeTerminal(input)
	if got != "subject redansi\tindent\nbody" {
		t.Fatalf("SanitizeTerminal() = %q, want controls removed and structural whitespace retained", got)
	}
}

func TestRenderedThreadMarkdownSanitizesMailText(t *testing.T) {
	payload := "\x1b]52;c;clipboard\a"
	thread := &RenderedThread{
		Subject: "subject" + payload,
		Messages: []RenderedMessage{{
			From:     "from" + payload,
			To:       "to" + payload,
			Date:     time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
			Markdown: "body " + payload + "\n[alt " + payload + "](https://example.test/" + payload + ")",
			Attachments: []Attachment{{
				N:        1,
				Filename: "report" + payload + ".pdf",
				MimeType: "application/pdf",
				Size:     1,
			}},
		}},
	}

	output := thread.Markdown()
	if strings.ContainsAny(output, "\x1b\a\x9b\x9c") || strings.Contains(output, "clipboard") {
		t.Fatalf("RenderedThread.Markdown() leaked terminal control payload: %q", output)
	}
	for _, want := range []string{"subject", "from", "to", "body", "alt", "report.pdf"} {
		if !strings.Contains(output, want) {
			t.Errorf("RenderedThread.Markdown() = %q, want %q retained", output, want)
		}
	}
}

func TestRenderTerminalMarkdownSanitizesBeforeGlamour(t *testing.T) {
	output, err := RenderTerminalMarkdown("body \x1b]52;c;clipboard\a", 80, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "clipboard") {
		t.Fatalf("RenderTerminalMarkdown() leaked terminal control payload: %q", output)
	}
	if !strings.Contains(output, "body") {
		t.Fatalf("RenderTerminalMarkdown() = %q, want body retained", output)
	}
}

func TestSanitizeTerminalRemovesBidiControls(t *testing.T) {
	input := "report\u202efdp.exe\u2066hidden\u2069\u200f"
	if got := SanitizeTerminal(input); got != "reportfdp.exehidden" {
		t.Fatalf("SanitizeTerminal(%q) = %q, want bidi controls removed before terminal display", input, got)
	}
}
