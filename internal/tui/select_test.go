package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func newListModel(t *testing.T, rows []*gmail.Thread) (app, *fakeAPI) {
	t.Helper()
	model, api := newTestApp(rows)
	model, _ = update(t, model, threadsMsg{request: model.currentRequest(listOperation), threads: rows})
	return model, api
}

func newThreadModel(t *testing.T, thread *gmail.Thread) app {
	t.Helper()
	model, _ := newListModel(t, []*gmail.Thread{thread})
	model.setSize(80, 20)
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	return model
}

func longThread(t *testing.T) *gmail.Thread {
	t.Helper()
	return threadFixture(99, strings.Repeat("<p>line</p>", 100))
}

func press(t *testing.T, model app, value string) (app, tea.Cmd) {
	t.Helper()
	return update(t, model, key(value))
}

func TestHashTrashesAndDIsNoop(t *testing.T) {
	model, _ := newListModel(t, testThreads(2))
	updated, cmd := press(t, model, "#")
	if updated.pending == nil || updated.pending.action != "trash" || !slices.Equal(updated.pending.ids, []string{model.list.rows[0].ID}) {
		t.Fatalf("# must start trash on cursor row, pending=%#v cmd=%v", updated.pending, cmd)
	}

	fresh, _ := newListModel(t, testThreads(1))
	after, cmd := press(t, fresh, "d")
	if after.pending != nil || cmd != nil {
		t.Fatal("d must be unbound in the list view")
	}
}

func TestThreadViewDIsNoopNotViewportScroll(t *testing.T) { // [G14]
	model := newThreadModel(t, longThread(t))
	before := model.viewport.YOffset
	model, cmd := press(t, model, "d")
	if model.pending != nil || cmd != nil {
		t.Fatal("d must not trash in the thread view")
	}
	if model.viewport.YOffset != before {
		t.Fatalf("d moved the viewport (YOffset %d → %d) — it must be consumed before the viewport handler", before, model.viewport.YOffset)
	}
	model, _ = press(t, model, "#")
	if model.pending == nil || model.pending.action != "trash" {
		t.Fatalf("# must trash the open thread, pending=%#v", model.pending)
	}
}

func TestSelectModeToggleAllClear(t *testing.T) {
	model, _ := newListModel(t, testThreads(3))
	model, _ = press(t, model, " ")
	if len(model.list.selected) != 0 {
		t.Fatal("space outside select mode must not select")
	}
	model, _ = press(t, model, "v")
	model, _ = press(t, model, " ")
	if _, ok := model.list.selected[model.list.rows[0].ID]; !ok || !model.list.selecting {
		t.Fatalf("v+space must toggle cursor row, selected=%v", model.list.selected)
	}
	model, _ = press(t, model, "a")
	if len(model.list.selected) != 3 {
		t.Fatalf("a must select all listed rows, got %v", model.list.selected)
	}
	model, _ = press(t, model, "esc")
	if model.list.selecting || len(model.list.selected) != 0 {
		t.Fatal("esc must exit select mode and clear the selection")
	}
}

func TestActionAppliesToSelectionElseCursor(t *testing.T) {
	model, _ := newListModel(t, testThreads(3))
	model, _ = press(t, model, "v")
	model, _ = press(t, model, " ")
	model, _ = press(t, model, "j")
	model, _ = press(t, model, " ")
	model, _ = press(t, model, "e")
	if model.pending == nil || !slices.Equal(model.pending.ids, []string{model.list.rows[0].ID, model.list.rows[1].ID}) {
		t.Fatalf("archive must target selection, pending=%#v", model.pending)
	}
}

func TestStaleSelectionRaceWritesNothing(t *testing.T) {
	model, api := newListModel(t, testThreads(2))
	model, _ = press(t, model, "v")
	model, _ = press(t, model, "a")
	model, _ = press(t, model, "R")
	if len(model.list.selected) != 0 {
		t.Fatal("beginListing must clear the selection synchronously, before the fetch")
	}
	model, cmd := press(t, model, "e")
	if model.pending != nil || cmd != nil {
		t.Fatal("action keys must do nothing while the listing generation is loading")
	}
	if len(api.modifyCalls) != 0 {
		t.Fatalf("refreshing selection started writes: %#v", api.modifyCalls)
	}
}

func TestPostRefreshSelectionRequired(t *testing.T) {
	rows := testThreads(2)
	model, _ := newListModel(t, rows)
	model, _ = press(t, model, "R")
	model, _ = update(t, model, threadsMsg{request: model.currentRequest(listOperation), threads: rows})
	if !model.listLoaded {
		t.Fatal("threadsMsg for the current generation must mark the listing loaded")
	}
	model, _ = press(t, model, "v")
	model, _ = press(t, model, " ")
	model, _ = press(t, model, "e")
	if model.pending == nil {
		t.Fatal("post-refresh selection must be actionable")
	}
}
