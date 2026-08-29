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

func TestSingleDigitLinkOpensImmediatelyWhenThreadHasAtMostNineLinks(t *testing.T) {
	thread := linkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalOpenURL := openURL
	t.Cleanup(func() { openURL = originalOpenURL })
	var opened string
	openURL = func(target string, _ []string) error {
		opened = target
		return nil
	}

	_, cmd := update(t, model, key("2"))
	runCmd(t, cmd)
	if got, want := opened, "https://example.test/two"; got != want {
		t.Fatalf("opened link = %q, want %q", got, want)
	}
}

func TestMultiDigitLinkOpensOnEnter(t *testing.T) {
	thread := tenLinkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalOpenURL := openURL
	t.Cleanup(func() { openURL = originalOpenURL })
	var opened string
	openURL = func(target string, _ []string) error {
		opened = target
		return nil
	}

	model, cmd := update(t, model, key("1"))
	if cmd != nil {
		t.Fatal("first digit opened a link instead of beginning link-number input")
	}
	if view := ansi.Strip(model.View()); !strings.Contains(view, "link number: 1") {
		t.Fatalf("reader view = %q, want link-number input in the status bar", view)
	}
	model, cmd = update(t, model, key("0"))
	if cmd != nil {
		t.Fatal("second digit returned an unexpected command")
	}
	model, cmd = update(t, model, key("enter"))
	msg := runCmd(t, cmd)
	model, _ = update(t, model, msg)
	if got, want := opened, "https://example.test/ten?email_token=secret"; got != want {
		t.Fatalf("opened link = %q, want full URL %q", got, want)
	}
}

func TestEscapeCancelsMultiDigitLinkInput(t *testing.T) {
	thread := tenLinkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})

	model, cmd := update(t, model, key("1"))
	if cmd != nil {
		t.Fatal("first digit opened a link instead of beginning link-number input")
	}
	model, _ = update(t, model, key("esc"))
	if model.view != threadView {
		t.Fatalf("escape during link-number input changed view to %v, want reader", model.view)
	}
	if strings.Contains(ansi.Strip(model.View()), "link number:") {
		t.Fatalf("reader view retained cancelled link-number input: %q", ansi.Strip(model.View()))
	}
}

func TestEscapeReturnsToInboxAfterOpeningMultiDigitLink(t *testing.T) {
	thread := tenLinkedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalOpenURL := openURL
	t.Cleanup(func() { openURL = originalOpenURL })
	openURL = func(string, []string) error { return nil }

	model, cmd := update(t, model, key("1"))
	if cmd != nil {
		t.Fatal("first digit opened a link instead of beginning link-number input")
	}
	model, _ = update(t, model, key("0"))
	model, cmd = update(t, model, key("enter"))
	model, _ = update(t, model, runCmd(t, cmd))
	model, _ = update(t, model, key("esc"))
	if model.view != listView {
		t.Fatalf("escape after link opening left view at %v, want inbox", model.view)
	}
}

func TestReaderNextThreadLoadsAndAdvancesListCursor(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[0]})

	model, cmd := update(t, model, key("n"))
	if model.view != threadView || model.list.cursor != 1 || !model.loading {
		t.Fatalf("next thread state = (view=%v cursor=%d loading=%v), want reader at second thread while loading", model.view, model.list.cursor, model.loading)
	}
	model, _ = update(t, model, runCmd(t, cmd))
	if model.thread.thread.ID != rows[1].ID || len(api.getCalls) != 1 || api.getCalls[0].id != rows[1].ID {
		t.Fatalf("next thread = %q, get calls = %#v, want second thread %q", model.thread.thread.ID, api.getCalls, rows[1].ID)
	}
}

func TestReaderNextAtLastThreadStaysInReader(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model.list.cursor = 1
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[1]})

	model, cmd := update(t, model, key("n"))
	if cmd != nil || model.view != threadView || model.list.cursor != 1 || model.status != "no newer threads" || len(api.getCalls) != 0 {
		t.Fatalf("next at last thread = (cmd=%v view=%v cursor=%d status=%q calls=%#v), want reader status without request", cmd != nil, model.view, model.list.cursor, model.status, api.getCalls)
	}
}

func TestReaderPreviousThreadLoadsAndMovesListCursor(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model.list.cursor = 1
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[1]})

	model, cmd := update(t, model, key("p"))
	if model.view != threadView || model.list.cursor != 0 || !model.loading {
		t.Fatalf("previous thread state = (view=%v cursor=%d loading=%v), want reader at first thread while loading", model.view, model.list.cursor, model.loading)
	}
	model, _ = update(t, model, runCmd(t, cmd))
	if model.thread.thread.ID != rows[0].ID || len(api.getCalls) != 1 || api.getCalls[0].id != rows[0].ID {
		t.Fatalf("previous thread = %q, get calls = %#v, want first thread %q", model.thread.thread.ID, api.getCalls, rows[0].ID)
	}
}

func TestReaderPreviousAtFirstThreadStaysInReader(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[0]})

	model, cmd := update(t, model, key("p"))
	if cmd != nil || model.view != threadView || model.list.cursor != 0 || model.status != "no older threads" || len(api.getCalls) != 0 {
		t.Fatalf("previous at first thread = (cmd=%v view=%v cursor=%d status=%q calls=%#v), want reader status without request", cmd != nil, model.view, model.list.cursor, model.status, api.getCalls)
	}
}

func TestEscapeAfterReaderNextReturnsToNavigatedThread(t *testing.T) {
	rows := testThreads(2)
	model, _ := newTestApp(rows)
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[0]})

	model, cmd := update(t, model, key("n"))
	model, _ = update(t, model, runCmd(t, cmd))
	model, _ = update(t, model, key("esc"))
	if model.view != listView || model.list.cursor != 1 {
		t.Fatalf("escape after next = (view=%v cursor=%d), want inbox on navigated second thread", model.view, model.list.cursor)
	}
}

func TestThreadViewDocumentsNextAndPreviousNavigation(t *testing.T) {
	model, _ := newTestApp(testThreads(1))

	if view := ansi.Strip(model.threadView()); !strings.Contains(view, "n/p") {
		t.Fatalf("thread view = %q, want next/previous key help", view)
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
	openURL = func(target string, _ []string) error {
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
	openURL = func(string, []string) error { return nil }

	model, linkCommand := update(t, model, key("1"))
	model, _ = update(t, model, key("e"))
	linkDone := runCmd(t, linkCommand)
	model, _ = update(t, model, linkDone)
	if !model.loading || model.pending == nil {
		t.Fatalf("link completion interrupted later action: loading=%v pending=%#v", model.loading, model.pending)
	}
}

func tenLinkedThread() *gmail.Thread {
	return threadFixture(1, `<p><a href="https://example.test/one">one</a> <a href="https://example.test/two">two</a> <a href="https://example.test/three">three</a> <a href="https://example.test/four">four</a> <a href="https://example.test/five">five</a> <a href="https://example.test/six">six</a> <a href="https://example.test/seven">seven</a> <a href="https://example.test/eight">eight</a> <a href="https://example.test/nine">nine</a> <a href="https://example.test/ten?email_token=secret">ten</a></p>`)
}
