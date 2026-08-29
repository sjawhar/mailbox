package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestLabelPickerUsesPrintableJKForFilteringAndArrowsForNavigation(t *testing.T) {
	rows := testThreads(1)
	model, _ := newTestApp(rows)
	model.ctx.labels = []gmail.Label{
		{ID: "Label_jira", Name: "Jira", Type: "user"},
		{ID: "Label_juno", Name: "Juno", Type: "user"},
		{ID: "Label_kappa", Name: "Kappa", Type: "user"},
		{ID: "Label_finance", Name: "Finance", Type: "user"},
	}

	model, _ = update(t, model, key("l"))
	model, _ = update(t, model, key("j"))
	if got, want := model.label.Value(), "j"; got != want {
		t.Fatalf("label filter after j = %q, want %q", got, want)
	}
	labels := model.filteredLabels()
	if len(labels) != 2 || labels[0].Name != "Jira" || labels[1].Name != "Juno" {
		t.Fatalf("labels filtered by j = %#v, want Jira and Juno", labels)
	}
	model, _ = update(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if got, want := model.labelCursor, 1; got != want {
		t.Fatalf("label cursor after down arrow = %d, want %d", got, want)
	}

	model, _ = update(t, model, key("esc"))
	model, _ = update(t, model, key("l"))
	model, _ = update(t, model, key("k"))
	if got, want := model.label.Value(), "k"; got != want {
		t.Fatalf("label filter after k = %q, want %q", got, want)
	}
	labels = model.filteredLabels()
	if len(labels) != 1 || labels[0].Name != "Kappa" {
		t.Fatalf("labels filtered by k = %#v, want Kappa", labels)
	}
}
