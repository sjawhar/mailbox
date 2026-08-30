package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestPreviewClipsLongUnbrokenLinesToPaneWidth(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.setSize(160, 45)
	model.preview.content = strings.Repeat("x", 500)

	if lines := strings.Count(ansi.Strip(model.View()), "\n") + 1; lines >= model.layout.height {
		t.Fatalf("long preview line expanded the split frame to %d rows", lines)
	}
}

func TestPreviewDebounceKeepsOnlyLatestCursorGeneration(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model.setSize(120, model.layout.height)
	first := model.requestPreview()
	model, second := update(t, model, key("j"))
	model, third := update(t, model, key("k"))

	for _, debounce := range []tea.Cmd{first, second, third} {
		request := runCmd(t, debounce)
		next, fetch := update(t, model, request)
		model = next
		if fetch != nil {
			runCmd(t, fetch)
		}
	}
	if len(api.getCalls) != 1 {
		t.Fatalf("preview GetThread calls = %#v, want only newest cursor generation", api.getCalls)
	}
}

func TestWideHelpIncludesSelectionBinding(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.setSize(160, model.layout.height)
	if !strings.Contains(ansi.Strip(model.View()), "v select") {
		t.Fatal("wide inbox help omits select-mode binding")
	}
}

func TestPreviewScopeErrorNamesConfiguredReadSource(t *testing.T) {
	api := &fakeAPI{threads: testThreads(1), getErr: &gmail.APIError{Status: 403, Reason: "insufficientPermissions", Message: "scope missing"}}
	model := newTestModel(api, "work")
	model.setSize(120, model.layout.height)
	model.preview.requestedID = api.threads[0].ID
	model.preview.loading = true
	model, fetch := update(t, model, previewRequestMsg{request: model.currentRequest(previewOperation), threadID: api.threads[0].ID})
	model, _ = update(t, model, runCmd(t, fetch))
	for _, want := range []string{"provision:", "accounts.work.read_credential_env", "gmail.readonly"} {
		if !strings.Contains(model.status, want) {
			t.Fatalf("preview scope error status = %q, want provisioning hint to contain %q", model.status, want)
		}
	}
}

func TestSplitPreviewFetchesCachesAndDiscardsStaleThread(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model.setSize(120, 30)
	model.preview.requestedID = rows[0].ID
	model.preview.loading = true

	model, command := update(t, model, previewRequestMsg{request: model.currentRequest(previewOperation), threadID: rows[0].ID})
	message := runCmd(t, command)
	model, _ = update(t, model, message)
	if !strings.Contains(ansi.Strip(model.preview.content), "Subject 1") || !strings.Contains(model.View(), "Preview") {
		t.Fatalf("preview = %q, view = %q, want rendered first thread in split pane", model.preview.content, model.View())
	}
	if len(api.getCalls) != 1 || api.getCalls[0].id != rows[0].ID {
		t.Fatalf("preview fetches = %#v, want first thread", api.getCalls)
	}

	model.preview.requestedID = rows[1].ID
	model.preview.content = ""
	model, _ = update(t, model, previewThreadMsg{request: model.currentRequest(previewOperation), threadID: rows[0].ID, thread: rows[0]})
	if model.preview.requestedID != rows[1].ID || strings.Contains(ansi.Strip(model.preview.content), "Subject 1") {
		t.Fatalf("stale preview changed current request: %#v", model.preview)
	}

	model.preview.requestedID = rows[0].ID
	model.preview.content = ""
	model, command = update(t, model, previewRequestMsg{request: model.currentRequest(previewOperation), threadID: rows[0].ID})
	if command != nil || !strings.Contains(ansi.Strip(model.preview.content), "Subject 1") || len(api.getCalls) != 1 {
		t.Fatalf("cached preview refetched or failed to render: cmd=%v calls=%#v content=%q", command, api.getCalls, model.preview.content)
	}
}

func TestCursorMoveDebouncesAndLoadsSplitPreview(t *testing.T) {
	rows := testThreads(2)
	model, _ := newTestApp(rows)
	model.setSize(120, model.layout.height)

	model, debounce := update(t, model, key("j"))
	if !model.preview.loading || model.preview.requestedID != rows[1].ID {
		t.Fatalf("cursor move did not schedule second-thread preview: %#v", model.preview)
	}
	request := runCmd(t, debounce)
	model, fetch := update(t, model, request)
	result := runCmd(t, fetch)
	model, _ = update(t, model, result)
	if !strings.Contains(ansi.Strip(model.preview.content), "Subject 2") {
		t.Fatalf("preview = %q, want second thread after cursor move", model.preview.content)
	}
}

func TestPreviewRerendersAfterResize(t *testing.T) {
	rows := testThreads(1)
	model, api := newTestApp(rows)
	model.setSize(120, model.layout.height)
	model.preview.requestedID = rows[0].ID
	model.preview.loading = true
	model, command := update(t, model, previewRequestMsg{request: model.currentRequest(previewOperation), threadID: rows[0].ID})
	model, _ = update(t, model, runCmd(t, command))

	model, debounce := update(t, model, tea.WindowSizeMsg{Width: 140, Height: 30})
	if debounce == nil {
		t.Fatal("resize reused a preview rendered at the previous width")
	}
	request := runCmd(t, debounce)
	model, fetch := update(t, model, request)
	model, _ = update(t, model, runCmd(t, fetch))
	if len(api.getCalls) != 2 || !strings.Contains(ansi.Strip(model.preview.content), "Subject 1") {
		t.Fatalf("resize preview fetches = %#v, content=%q", api.getCalls, model.preview.content)
	}
}
