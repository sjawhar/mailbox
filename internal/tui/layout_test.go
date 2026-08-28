package tui

import (
	"crypto/sha256"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/sjawhar/mailbox/internal/gmail"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestStandardViewsMatchCapturedBaseline(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "dark")

	rows := testThreads(2)
	model, _ := newTestApp(rows)
	labelRows := testThreads(1)
	labelRows[0].Messages[0].LabelIDs = append(labelRows[0].Messages[0].LabelIDs, "Label_7")
	labelModel, _ := newTestApp(labelRows)
	thread := linkedThread()
	reader, _ := newTestApp([]*gmail.Thread{thread})
	reader.thread.thread = thread
	reader.viewport.Width = 80
	if err := reader.renderCurrentThread(); err != nil {
		t.Fatal(err)
	}

	views := map[string]string{
		"rows-standard": model.list.rowsView(80, nil, 6),
		"rows-label":    labelModel.list.rowsView(80, map[string]string{"Label_7": "Project"}, 6),
		"reader":        reader.viewport.View(),
	}
	baselineRawSHA256 := map[string]string{
		"rows-standard": "fa9f9a41860bc13c47d6859a5d2b90bab3517fb4104837be3c9ea25bdc1f625d",
		"rows-label":    "c38d28f4478de20e89eace3ac4d17751d69c6c37e9f70c5e48cf647f3f09fa0f",
		"reader":        "44ff1df3b975e1146af013e1b3fe65cd44de0618de2e989db8bd55769aa898bf",
	}
	baselineStrippedSHA256 := map[string]string{
		"rows-standard": "fa9f9a41860bc13c47d6859a5d2b90bab3517fb4104837be3c9ea25bdc1f625d",
		"rows-label":    "c38d28f4478de20e89eace3ac4d17751d69c6c37e9f70c5e48cf647f3f09fa0f",
		"reader":        "7b922b17060a9ac72fbee104f58b1fafffdb9ce48ecc05b714982cf75d8e2642",
	}
	for name, view := range views {
		if view == "" {
			t.Fatalf("%s view is empty", name)
		}
		if got, want := fmt.Sprintf("%x", sha256.Sum256([]byte(view))), baselineRawSHA256[name]; got != want {
			t.Fatalf("%s raw view SHA-256 = %s, want captured %s", name, got, want)
		}
		stripped := ansi.Strip(view)
		if got, want := fmt.Sprintf("%x", sha256.Sum256([]byte(stripped))), baselineStrippedSHA256[name]; got != want {
			t.Fatalf("%s stripped view SHA-256 = %s, want captured %s", name, got, want)
		}
		t.Logf("%s raw-sha256=%s stripped-sha256=%s", name, baselineRawSHA256[name], baselineStrippedSHA256[name])
	}
}

func TestLayoutMetricsDeriveSplitPaneDimensionsOncePerResize(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.setSize(160, 45)

	if got, want := model.layout.listPaneWidth, 76; got != want {
		t.Fatalf("list pane width = %d, want %d", got, want)
	}
	if got, want := model.layout.previewPaneWidth, 75; got != want {
		t.Fatalf("preview pane width = %d, want %d", got, want)
	}
	if got, want := model.layout.splitPaneHeight, 39; got != want {
		t.Fatalf("split pane height = %d, want %d", got, want)
	}
	if got, want := model.layout.listContentWidth, 74; got != want {
		t.Fatalf("list content width = %d, want %d", got, want)
	}
	if got, want := model.layout.previewContentWidth, 73; got != want {
		t.Fatalf("preview content width = %d, want %d", got, want)
	}
	if got, want := model.layout.splitContentHeight, 37; got != want {
		t.Fatalf("split content height = %d, want %d", got, want)
	}
	if model.search.Width != model.layout.searchInputWidth || model.label.Width != model.layout.labelInputWidth {
		t.Fatalf("input widths = (%d, %d), want layout metrics (%d, %d)", model.search.Width, model.label.Width, model.layout.searchInputWidth, model.layout.labelInputWidth)
	}
}

func TestLabelCacheBuildsUserLabelNamesForRows(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	labels := []gmail.Label{
		{ID: "Label_7", Name: "Project", Type: "user"},
		{ID: "INBOX", Name: "Inbox", Type: "system"},
	}

	model, _ = update(t, model, labelsMsg{request: model.currentRequest(labelOperation), labels: labels})
	if got, want := model.ctx.labelNameByID, map[string]string{"Label_7": "Project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("label names = %#v, want %#v", got, want)
	}
}

func TestThreadFixtureUsesStableGmailShapedIDs(t *testing.T) {
	rows := testThreads(25)
	gmailID := regexp.MustCompile(`^[0-9a-f]{16}$`)
	for index, thread := range rows {
		if !gmailID.MatchString(thread.ID) {
			t.Fatalf("thread %d ID = %q, want 16 lowercase hexadecimal digits", index+1, thread.ID)
		}
		message := thread.Messages[0]
		if !gmailID.MatchString(message.ID) || message.ThreadID != thread.ID {
			t.Fatalf("thread %d message = (%q, %q), want Gmail-shaped message ID and matching thread ID", index+1, message.ID, message.ThreadID)
		}
	}
}
func TestSplitViewComposesListAndPreviewAtPTYSize(t *testing.T) {
	model, _ := newTestApp(testThreads(2))
	model.setSize(160, 45)
	model.preview.content = "preview body"

	view := ansi.Strip(model.View())
	for _, text := range []string{"Sender 1 <sender@example.test>", "Subject 1", "Preview", "preview body"} {
		if !strings.Contains(view, text) {
			t.Fatalf("split view missing %q:\n%s", text, view)
		}
	}
}

func TestSplitViewConstrainsLongPreviewToPaneHeight(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.setSize(160, 45)
	model.preview.content = strings.Repeat("preview line\n", 100)

	view := ansi.Strip(model.View())
	if strings.Count(view, "\n") >= model.layout.height+4 {
		t.Fatalf("split view overflowed terminal height: %d lines for %d rows", strings.Count(view, "\n")+1, model.layout.height)
	}
	if !strings.Contains(view, "Sender 1 <sender@example.test>") {
		t.Fatalf("long preview hid the list pane:\n%s", view)
	}
}

func TestMiddleTruncatePreservesAddressTailOrder(t *testing.T) {
	sender := "synthetic-notifier[bot] <notifications@github.com>"
	if got, want := truncateSender(sender, 45), "synthetic-notifier[bot] <notificati…thub.com>"; got != want {
		t.Fatalf("truncateSender() = %q, want %q", got, want)
	}
}

func TestSplitListRowsNeverWrapDateColumn(t *testing.T) {
	rows := testThreads(1)
	rows[0].Messages[0].Payload.Headers[0].Value = "synthetic-notifier[bot] <notifications@github.com>"
	rows[0].Messages[0].Payload.Headers[2].Value = strings.Repeat("Long subject ", 12)
	model, _ := newTestApp(rows)
	model.setSize(160, 45)

	if view := ansi.Strip(model.View()); strings.Contains(view, "\n│ 29") {
		t.Fatalf("split list wrapped date into a second line:\n%s", view)
	}
}

func TestSplitViewFitsTerminalHeight(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.setSize(160, 45)
	model.preview.content = "preview"

	if lines := strings.Count(ansi.Strip(model.View()), "\n") + 1; lines >= model.layout.height {
		t.Fatalf("split view has %d lines for a %d-line terminal; reserve a renderer row", lines, model.layout.height)
	}
}
