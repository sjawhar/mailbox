package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestLabelPickerRendersOnlyUserLabelsWithThreadUnionMarker(t *testing.T) {
	project := gmail.Label{ID: "Label_project", Name: "Project", Type: "user"}
	rows := testThreads(1)
	rows[0].Messages = append(rows[0].Messages, &gmail.Message{ID: "second-message", LabelIDs: []string{project.ID}})
	model, _ := newTestApp(rows)

	model, _ = update(t, model, key("l"))
	model, _ = update(t, model, labelsMsg{
		request: model.currentRequest(labelOperation),
		labels: []gmail.Label{
			{ID: "INBOX", Name: "INBOX", Type: "system"},
			project,
			{ID: "Label_finance", Name: "Finance", Type: "user"},
		},
	})
	view := ansi.Strip(model.labelPickerView())
	if strings.Contains(view, "INBOX") {
		t.Fatalf("label picker listed system label: %q", view)
	}
	if !strings.Contains(view, ">* Project") || !strings.Contains(view, "   Finance") {
		t.Fatalf("label picker = %q, want user labels with Project marked active", view)
	}
}
