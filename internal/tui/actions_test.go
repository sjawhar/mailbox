package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestCursorAndSelect(t *testing.T) {
	rows := testThreads(3)
	model, _ := newTestApp(rows)
	model, _ = update(t, model, threadsMsg{request: model.currentRequest(listOperation), threads: rows})
	for _, msg := range []tea.Msg{key("j"), key("j"), key(" "), key("k"), key(" ")} {
		model, _ = update(t, model, msg)
	}

	if model.list.cursor != 1 {
		t.Fatalf("cursor = %d, want row 2", model.list.cursor)
	}
	want := map[string]struct{}{rows[1].ID: {}, rows[2].ID: {}}
	if !reflect.DeepEqual(model.list.selected, want) {
		t.Fatalf("selection = %#v, want %#v", model.list.selected, want)
	}
}

func TestArchiveSelectionCallsAPI(t *testing.T) {
	rows := testThreads(3)
	model, api := newTestApp(rows)
	model.list.selected = map[string]struct{}{rows[0].ID: {}, rows[2].ID: {}}

	model, cmd := update(t, model, key("e"))
	msg := runCmd(t, cmd)
	if len(api.modifyCalls) != 1 {
		t.Fatalf("ModifyThreads calls = %#v, want one", api.modifyCalls)
	}
	if got, want := api.modifyCalls[0], (modifyCall{ids: []string{rows[0].ID, rows[2].ID}, remove: []string{"INBOX"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModifyThreads call = %#v, want %#v", got, want)
	}
	model, _ = update(t, model, msg)
	if got := threadIDs(model.list.rows); !reflect.DeepEqual(got, []string{rows[1].ID}) {
		t.Fatalf("rows after archive = %v, want [%s]", got, rows[1].ID)
	}
}

func TestArchiveStartsSpinner(t *testing.T) {
	model, _ := newTestApp(testThreads(1))

	_, command := update(t, model, key("e"))
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("archive command message = %T, want tea.BatchMsg", message)
	}
	var action, ticking bool
	for _, command := range batch {
		switch command().(type) {
		case actionDoneMsg:
			action = true
		case spinner.TickMsg:
			ticking = true
		}
	}
	if !action || !ticking {
		t.Fatalf("archive command action = %v, ticking = %v", action, ticking)
	}
}

func TestArchiveCursorWhenNoSelection(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model.list.cursor = 1

	_, cmd := update(t, model, key("e"))
	runCmd(t, cmd)
	if got, want := api.modifyCalls[0].ids, []string{rows[1].ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archive IDs = %v, want %v", got, want)
	}
}

func TestToggleUnread(t *testing.T) {
	rows := testThreads(1)
	model, api := newTestApp(rows)

	_, cmd := update(t, model, key("u"))
	runCmd(t, cmd)
	if got, want := api.modifyCalls[0], (modifyCall{ids: []string{rows[0].ID}, remove: []string{"UNREAD"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModifyThreads call = %#v, want %#v", got, want)
	}
}

func TestSearchPrompt(t *testing.T) {
	model, api := newTestApp(testThreads(1))
	model, _ = update(t, model, key("/"))
	model, _ = update(t, model, key("from:alice"))
	_, cmd := update(t, model, key("enter"))
	runCmd(t, cmd)

	if len(api.listCalls) != 1 {
		t.Fatalf("ListThreads calls = %d, want 1", len(api.listCalls))
	}
	if got, want := api.listCalls[0].Query, "from:alice"; got != want {
		t.Fatalf("search query = %q, want %q", got, want)
	}
	if got := api.listCalls[0].LabelIDs; len(got) != 0 {
		t.Fatalf("search label filter = %v, want none", got)
	}
}

func TestRefresh(t *testing.T) {
	model, api := newTestApp(testThreads(1))
	model.list.query = "from:alice"

	_, cmd := update(t, model, key("R"))
	runCmd(t, cmd)
	if len(api.listCalls) != 1 || api.listCalls[0].Query != "from:alice" {
		t.Fatalf("refresh ListThreads calls = %#v, want query from:alice", api.listCalls)
	}
}

func TestEnterOpensThread(t *testing.T) {
	rows := testThreads(1)
	model, api := newTestApp(rows)

	model, cmd := update(t, model, key("enter"))
	msg := runCmd(t, cmd)
	if got, want := api.getCalls[0], (getCall{id: rows[0].ID, format: "full"}); got != want {
		t.Fatalf("GetThread call = %#v, want %#v", got, want)
	}
	model, _ = update(t, model, msg)
	if model.view != threadView || !strings.Contains(ansi.Strip(model.viewport.View()), "Subject 1") {
		t.Fatalf("thread view = %v, viewport = %q, want rendered Subject 1", model.view, model.viewport.View())
	}
}

func TestActAndAdvance(t *testing.T) {
	rows := testThreads(3)
	model, api := newTestApp(rows)
	model.list.cursor = 1
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[1]})

	model, cmd := update(t, model, key("e"))
	action := runCmd(t, cmd)
	model, next := update(t, model, action)
	if got, want := api.modifyCalls[0], (modifyCall{ids: []string{rows[1].ID}, remove: []string{"INBOX"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModifyThreads call = %#v, want %#v", got, want)
	}
	if !model.loading {
		t.Fatal("advancing to the next reader thread did not start loading")
	}
	runCmd(t, next)
	if got, want := api.getCalls[len(api.getCalls)-1], (getCall{id: rows[2].ID, format: "full"}); got != want {
		t.Fatalf("next GetThread call = %#v, want %#v", got, want)
	}
}

func TestActAndAdvanceLastReturnsToList(t *testing.T) {
	rows := testThreads(2)
	model, api := newTestApp(rows)
	model.list.cursor = 1
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[1]})

	model, cmd := update(t, model, key("d"))
	action := runCmd(t, cmd)
	model, next := update(t, model, action)
	if got, want := api.trashCalls[0], []string{rows[1].ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TrashThreads IDs = %v, want %v", got, want)
	}
	if model.view != listView || next != nil {
		t.Fatalf("post-trash view = %v, cmd = %v, want list view without next fetch", model.view, next)
	}
}

func TestQuoteToggleRerenders(t *testing.T) {
	thread := quotedThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	if strings.Contains(ansi.Strip(model.viewport.View()), "quoted marker") {
		t.Fatal("default thread render retained quoted history")
	}

	model, _ = update(t, model, key("Q"))
	if !model.thread.keepQuotes || !strings.Contains(ansi.Strip(model.viewport.View()), "quoted marker") {
		t.Fatalf("quote toggle = %v, viewport = %q, want quote marker", model.thread.keepQuotes, model.viewport.View())
	}
}

func TestPendingActionBlocksSecondWrite(t *testing.T) {
	rows := testThreads(1)
	model, api := newTestApp(rows)
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: rows[0]})

	model, archive := update(t, model, key("e"))
	pending := model.pending
	model, second := update(t, model, key("d"))
	if second != nil || model.pending != pending {
		t.Fatalf("second action started while archive was pending: cmd=%v pending=%#v", second, model.pending)
	}

	action := runCmd(t, archive)
	model, next := update(t, model, action)
	if len(api.modifyCalls) != 1 || len(api.trashCalls) != 0 {
		t.Fatalf("API calls after serialized action: modify=%#v trash=%#v", api.modifyCalls, api.trashCalls)
	}
	if model.view != listView || len(model.list.rows) != 0 || next != nil {
		t.Fatalf("completed archive did not update the list once: view=%v rows=%v cmd=%v", model.view, threadIDs(model.list.rows), next)
	}
}

func TestLabelPickerToggle(t *testing.T) {
	label := gmail.Label{ID: "Label_7", Name: "Project", Type: "user"}
	rows := testThreads(2)
	rows[0].Messages[0].LabelIDs = append(rows[0].Messages[0].LabelIDs, label.ID)
	model, api := newTestApp(rows)
	model.ctx.labels = []gmail.Label{label}
	model.list.selected = map[string]struct{}{rows[0].ID: {}, rows[1].ID: {}}

	model, _ = update(t, model, key("l"))
	_, cmd := update(t, model, key("enter"))
	runCmd(t, cmd)
	if got, want := api.modifyCalls[0], (modifyCall{ids: []string{rows[0].ID, rows[1].ID}, add: []string{label.ID}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("label add call = %#v, want %#v", got, want)
	}

	rows[1].Messages[0].LabelIDs = append(rows[1].Messages[0].LabelIDs, label.ID)
	model, api = newTestApp(rows)
	model.ctx.labels = []gmail.Label{label}
	model.list.selected = map[string]struct{}{rows[0].ID: {}, rows[1].ID: {}}
	model, _ = update(t, model, key("l"))
	_, cmd = update(t, model, key("enter"))
	runCmd(t, cmd)
	if got, want := api.modifyCalls[0], (modifyCall{ids: []string{rows[0].ID, rows[1].ID}, remove: []string{label.ID}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("label remove call = %#v, want %#v", got, want)
	}
}

func TestLabelPickerUsesASCIIMarker(t *testing.T) {
	label := gmail.Label{ID: "Label_7", Name: "Project", Type: "user"}
	rows := testThreads(1)
	rows[0].Messages[0].LabelIDs = append(rows[0].Messages[0].LabelIDs, label.ID)
	model, _ := newTestApp(rows)
	model.ctx.labels = []gmail.Label{label}
	model.list.selected = map[string]struct{}{rows[0].ID: {}}

	model, _ = update(t, model, key("l"))
	view := model.labelPickerView()
	if !strings.Contains(view, ">* Project") {
		t.Fatalf("label picker = %q, want ASCII selected marker", view)
	}
	if strings.Contains(view, "✓") {
		t.Fatalf("label picker contains non-ASCII marker: %q", view)
	}
}
func TestEmptyLabelsAreCached(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.ctx.labels = nil

	model, _ = update(t, model, labelsMsg{request: model.currentRequest(labelOperation), labels: nil})
	if model.ctx.labels == nil {
		t.Fatal("empty label response was not cached")
	}
}
