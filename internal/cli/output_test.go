package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/render"
)

func TestPrintThreadsPlain(t *testing.T) {
	var output bytes.Buffer
	printThreads(&output, []threadRow{{N: 1, ID: "t1", Unread: true, From: "Alice", Subject: "Hello", Date: "2026-08-27T01:02:03Z"}}, false)
	if got, want := output.String(), "1\tt1\ttrue\tAlice\tHello\t2026-08-27T01:02:03Z\n"; got != want {
		t.Fatalf("plain row = %q, want %q", got, want)
	}
}

func TestPrintThreadsPretty(t *testing.T) {
	var output bytes.Buffer
	printThreads(&output, []threadRow{{N: 1, ID: "t1", Unread: true, From: "Alice", Subject: "Hello", Date: "2026-08-27T01:02:03Z"}}, true)
	if !strings.Contains(output.String(), "Hello") {
		t.Fatalf("pretty row = %q, want subject", output.String())
	}
}

func TestPrintThreadsUsesSharedSenderDisplayName(t *testing.T) {
	var output bytes.Buffer
	printThreads(&output, []threadRow{{
		N:       1,
		ID:      "t1",
		From:    `"Example Commenter (via Google Docs)" <comments-noreply@docs.google.com>`,
		Subject: "Hello",
		Date:    "2026-08-27T01:02:03Z",
	}}, false)

	if got, want := output.String(), "1\tt1\tfalse\tExample Commenter (via Google Docs) <comments-noreply@docs.google.com>\tHello\t2026-08-27T01:02:03Z\n"; got != want {
		t.Fatalf("plain row = %q, want %q", got, want)
	}
}

func TestPrintThreadsSanitizesMailText(t *testing.T) {
	var output bytes.Buffer
	printThreads(&output, []threadRow{{
		N:       1,
		ID:      "t1",
		From:    "Alice\x1b]52;c;clipboard\a",
		Subject: "Status\x1b[8mspoof\x1b[0m",
		Date:    "2026-08-27T01:02:03Z",
	}}, true)

	if strings.Contains(output.String(), "clipboard") || strings.Contains(output.String(), "\x1b") {
		t.Fatalf("printThreads() leaked terminal control payload: %q", output.String())
	}
	if !strings.Contains(output.String(), "Alice") || !strings.Contains(output.String(), "Statusspoof") {
		t.Fatalf("printThreads() = %q, want safe sender and subject text", output.String())
	}
}

func TestAttachmentListSanitizesMailFilename(t *testing.T) {
	var output bytes.Buffer
	ctx := &cmdCtx{stdout: &output, text: true}
	if code := ctx.attachmentList("work", nil, "thread", []render.Attachment{{
		N:        1,
		Filename: "report\x1b]52;c;clipboard\a.pdf",
		MimeType: "application/pdf",
	}}); code != 0 {
		t.Fatalf("attachmentList() exit = %d, want 0", code)
	}
	if strings.Contains(output.String(), "clipboard") || strings.Contains(output.String(), "\x1b]52") {
		t.Fatalf("attachmentList() leaked terminal control payload: %q", output.String())
	}
}
