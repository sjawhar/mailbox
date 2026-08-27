package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestRenderCurrentThreadUsesExplicitGlamourStyle(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")
	thread := linkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.thread.thread = thread

	if err := model.renderCurrentThread(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.viewport.View(), "\x1b[") {
		t.Fatalf("rendered thread = %q, want explicit Glamour ANSI styling", model.viewport.View())
	}
}

func TestThreadHeadersRenderOutsideMarkdown(t *testing.T) {
	thread := linkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.thread.thread = thread
	model.viewport.Width = 120

	if err := model.renderCurrentThread(); err != nil {
		t.Fatal(err)
	}
	rendered := ansi.Strip(model.viewport.View())
	wantHeader := "Sender 1 <sender@example.test> → Receiver <receiver@example.test> · 2026-08-29 10:40 UTC"
	if !strings.Contains(rendered, wantHeader) {
		t.Fatalf("rendered thread = %q, want header %q", rendered, wantHeader)
	}
	if strings.Contains(rendered, "mailto:") || strings.Contains(rendered, "## Sender") {
		t.Fatalf("markdown syntax leaked into thread header: %q", rendered)
	}
}

func TestDigitOpensLink(t *testing.T) {
	thread := linkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalOpenURL := openURL
	t.Cleanup(func() { openURL = originalOpenURL })
	var opened string
	openURL = func(target string) error {
		opened = target
		return nil
	}

	_, cmd := update(t, model, key("2"))
	runCmd(t, cmd)
	if got, want := opened, "https://example.test/two"; got != want {
		t.Fatalf("opened link = %q, want %q", got, want)
	}
}

func TestHTMLBackstopOpensSanitizedDocument(t *testing.T) {
	thread := threadFixture(1, `<script>steal()</script><a href="https://example.test/two" onclick="steal()">two</a><img src="https://tracker.example/pixel">`)
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalOpenURL := openURL
	t.Cleanup(func() { openURL = originalOpenURL })
	var opened string
	openURL = func(target string) error {
		opened = target
		return nil
	}

	model, cmd := update(t, model, key("o"))
	msg := runCmd(t, cmd)
	model, _ = update(t, model, msg)
	t.Cleanup(func() {
		if opened != "" {
			if err := os.Remove(opened); err != nil {
				t.Errorf("remove opened HTML file: %v", err)
			}
		}
	})
	contents, err := os.ReadFile(opened)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "two") || !strings.Contains(model.status, opened) {
		t.Fatalf("opened HTML = %q, status = %q, want rendered original HTML path", contents, model.status)
	}
	for _, forbidden := range []string{"steal()", "onclick=", "https://tracker.example", "https://example.test/two"} {
		if strings.Contains(string(contents), forbidden) {
			t.Errorf("opened HTML retained active content %q: %q", forbidden, contents)
		}
	}
}

func TestOpenCompletionClearsSpinner(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.loading = true

	model, _ = update(t, model, openedMsg{request: model.currentRequest(openOperation), target: "/tmp/mailbox.html", clearLoading: true})
	if model.loading {
		t.Fatal("successful HTML open left the spinner active")
	}
}

func TestLinkCompletionDoesNotClearLaterActionSpinner(t *testing.T) {
	thread := linkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalOpenURL := openURL
	t.Cleanup(func() { openURL = originalOpenURL })
	openURL = func(string) error { return nil }

	model, linkCommand := update(t, model, key("1"))
	model, _ = update(t, model, key("e"))
	linkDone := runCmd(t, linkCommand)
	model, _ = update(t, model, linkDone)
	if !model.loading || model.pending == nil {
		t.Fatalf("link completion interrupted later action: loading=%v pending=%#v", model.loading, model.pending)
	}
}
