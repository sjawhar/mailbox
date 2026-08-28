package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestInitStartsListingAndSpinner(t *testing.T) {
	model, api := newTestApp(testThreads(1))
	model.loading = true

	message := model.Init()()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init() message = %T, want tea.BatchMsg", message)
	}
	if len(batch) != 2 {
		t.Fatalf("Init() commands = %d, want listing and spinner", len(batch))
	}

	var listing, ticking bool
	for _, command := range batch {
		switch command().(type) {
		case threadsMsg:
			listing = true
		case spinner.TickMsg:
			ticking = true
		}
	}
	if !listing || !ticking || len(api.listCalls) != 1 {
		t.Fatalf("Init() listing = %v, ticking = %v, calls = %d", listing, ticking, len(api.listCalls))
	}
}

func TestInboxListingExcludesThreadsWithoutInboxMessage(t *testing.T) {
	threads := testThreads(2)
	threads[1].Messages[0].LabelIDs = []string{"SENT"}
	model, _ := newTestApp(threads)

	message := runCmd(t, listThreadsCmd(model.currentRequest(listOperation), ""))
	listing, ok := message.(threadsMsg)
	if !ok {
		t.Fatalf("listThreadsCmd() = %T, want threadsMsg", message)
	}
	if got, want := threadIDs(listing.threads), []string{threads[0].ID}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("listed IDs = %v, want %v", got, want)
	}
}

func TestTruncatePreservesGraphemesAndDisplayWidth(t *testing.T) {
	tests := []struct {
		width int
		want  string
	}{
		{width: 1, want: "…"},
		{width: 3, want: "界…"},
		{width: 5, want: "界界…"},
		{width: 6, want: "界界é…"},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			if got := truncate("界界éxy", test.width); got != test.want {
				t.Fatalf("truncate() = %q, want %q", got, test.want)
			}
			if strings.Contains(truncate("界界éxy", test.width), "�") {
				t.Fatalf("truncate() introduced a replacement character at width %d", test.width)
			}
		})
	}
}

func TestMetadataUsesSharedHeadersAndLatestMessageDate(t *testing.T) {
	older := &gmail.Message{
		InternalDate: time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC).UnixMilli(),
		Payload:      &gmail.MessagePart{Headers: []gmail.Header{{Name: "From", Value: "Old <old@example.com>"}, {Name: "Subject", Value: "Old"}}},
	}
	newer := &gmail.Message{
		InternalDate: time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC).UnixMilli(),
		Payload: &gmail.MessagePart{Headers: []gmail.Header{
			{Name: "From", Value: `"Example Commenter (via Google Docs)" <comments-noreply@docs.google.com>`},
			{Name: "Subject", Value: "=?ISO-8859-1?Q?R=E9sum=E9_for_Jos=E9?="},
		}},
	}

	from, subject, date := metadata(&gmail.Thread{Messages: []*gmail.Message{older, newer}})
	if from != "Example Commenter (via Google Docs) <comments-noreply@docs.google.com>" || subject != "Résumé for José" || date != "Aug 26" {
		t.Fatalf("metadata() = (%q, %q, %q), want newest decoded message", from, subject, date)
	}
}

func TestRowsRenderSenderAndSubjectOnSeparateLines(t *testing.T) {
	rows := testThreads(1)
	rows[0].Messages[0].LabelIDs = append(rows[0].Messages[0].LabelIDs, "STARRED", "Label_7")
	model, _ := newTestApp(rows)
	rendered := ansi.Strip(model.list.rowsView(40, nil, 2))
	lines := strings.Split(rendered, "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "Aug 29") || !strings.Contains(lines[0], "…") || !strings.Contains(lines[1], "Subject 1") || strings.Contains(lines[1], "STARRED") || strings.Contains(lines[1], "Label_7") {
		t.Fatalf("two-line row = %#v", lines)
	}
}

func TestConstrainedChipRowHasNoOrphanedANSI(t *testing.T) {
	rows := testThreads(1)
	rows[0].Messages[0].LabelIDs = append(rows[0].Messages[0].LabelIDs, "Label_7")
	model, _ := newTestApp(rows)
	line := model.list.rowsView(30, map[string]string{"Label_7": "Project"}, 2)
	if strings.Contains(line, "\x1b") && !strings.HasSuffix(line, "\x1b[0m") {
		t.Fatalf("truncated chip styling lacks reset: %q", line)
	}
}

func TestSelectedUnreadStylesBothLines(t *testing.T) {
	rows := testThreads(1)
	model, _ := newTestApp(rows)
	model.list.selected[rows[0].ID] = struct{}{}
	lines := strings.Split(model.list.rowsView(80, nil, 2), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], ">*") || !strings.HasPrefix(lines[1], "*") {
		t.Fatalf("selected unread styles = %#v", lines)
	}
}

func TestOddHeightSearchFitsScreen(t *testing.T) {
	model, _ := newTestApp(testThreads(25))
	model.setSize(100, 45)
	model.view = searchView
	if lines := strings.Count(ansi.Strip(model.View()), "\n") + 1; lines > 45 {
		t.Fatalf("search view lines = %d", lines)
	}
}

func TestSelectedReadSenderIsNotBold(t *testing.T) {
	rows := testThreads(1)
	rows[0].Messages[0].LabelIDs = []string{"INBOX"}
	model, _ := newTestApp(rows)
	model.list.selected[rows[0].ID] = struct{}{}
	line := model.list.rowsView(80, nil, 2)
	if strings.Contains(line, "\x1b[1m") {
		t.Fatalf("selected read row became bold: %q", line)
	}
}

func TestSplitSenderColumnKeepsUsefulAddress(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.setSize(130, 30)

	if got := ansi.Strip(model.View()); !strings.Contains(got, "Sender 1 <sender@example.test>") {
		t.Fatalf("split list = %q, want sender name and complete common address", got)
	}
}

func TestErrorSurfacesInStatusBar(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	model.ctx.lastRoute = func() auth.Route { return auth.RouteBroker }
	err := &gmail.APIError{Status: 403, Reason: "insufficientPermissions", Message: "scope missing"}

	model, cmd := update(t, model, errMsg{request: model.currentRequest(listOperation), err: err})
	for _, want := range []string{"provision:", "read-only", "GWS_WORK_MODIFY_OAUTH"} {
		if !strings.Contains(model.status, want) {
			t.Fatalf("status = %q, want broker provisioning hint to contain %q", model.status, want)
		}
	}
	if cmd != nil {
		t.Fatal("error handling returned a quit command")
	}
}
