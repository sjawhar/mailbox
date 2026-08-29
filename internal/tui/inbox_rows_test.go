package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	newerTime := time.Now().Local().AddDate(0, 0, -1)
	older := &gmail.Message{
		InternalDate: newerTime.AddDate(0, 0, -1).UnixMilli(),
		Payload:      &gmail.MessagePart{Headers: []gmail.Header{{Name: "From", Value: "Old <old@example.com>"}, {Name: "Subject", Value: "Old"}}},
	}
	newer := &gmail.Message{
		InternalDate: newerTime.UnixMilli(),
		Payload: &gmail.MessagePart{Headers: []gmail.Header{
			{Name: "From", Value: `"Example Commenter (via Google Docs)" <comments-noreply@docs.google.com>`},
			{Name: "Subject", Value: "=?ISO-8859-1?Q?R=E9sum=E9_for_Jos=E9?="},
		}},
	}

	from, subject, date := metadata(&gmail.Thread{Messages: []*gmail.Message{older, newer}})
	if from != "Example Commenter (via Google Docs) <comments-noreply@docs.google.com>" || subject != "Résumé for José" || date != newerTime.Format("Jan 02") {
		t.Fatalf("metadata() = (%q, %q, %q), want newest decoded message", from, subject, date)
	}
}

func TestMetadataFormatsTodayAsTimeAndOlderMessageAsDate(t *testing.T) {
	today := time.Now().Local()
	today = time.Date(today.Year(), today.Month(), today.Day(), 12, 34, 0, 0, time.Local)
	older := today.AddDate(0, 0, -1)

	todayThread := threadFixture(1, "")
	todayThread.Messages[0].InternalDate = today.UnixMilli()
	olderThread := threadFixture(2, "")
	olderThread.Messages[0].InternalDate = older.UnixMilli()
	_, _, todayDate := metadata(todayThread)
	_, _, olderDate := metadata(olderThread)

	if got, want := todayDate, "12:34"; got != want {
		t.Fatalf("today metadata date = %q, want 24-hour time %q", got, want)
	}
	if got, want := olderDate, older.Format("Jan 02"); got != want {
		t.Fatalf("older metadata date = %q, want date %q", got, want)
	}
}

func TestRowsRenderSenderAndSubjectOnSeparateLines(t *testing.T) {
	rows := testThreads(1)
	rows[0].Messages[0].LabelIDs = append(rows[0].Messages[0].LabelIDs, "STARRED", "Label_7")
	model, _ := newTestApp(rows)
	_, _, date := metadata(rows[0])
	rendered := ansi.Strip(model.list.rowsView(40, nil, 2))
	lines := strings.Split(rendered, "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "Sender 1 <sender@example.test>") || strings.Contains(lines[0], date) || !strings.Contains(lines[1], "Subject 1") || !strings.HasSuffix(lines[1], date) || strings.Contains(lines[1], "STARRED") || strings.Contains(lines[1], "Label_7") {
		t.Fatalf("two-line row = %#v", lines)
	}
}

func TestNarrowRowsPreserveDateWhileTruncatingSubject(t *testing.T) {
	rows := testThreads(1)
	rows[0].Messages[0].InternalDate = time.Date(2026, time.August, 22, 12, 0, 0, 0, time.Local).UnixMilli()
	rows[0].Messages[0].Payload.Headers[2].Value = "A subject that cannot fit"
	model, _ := newTestApp(rows)

	lines := strings.Split(ansi.Strip(model.list.rowsView(24, nil, 2)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "…") || !strings.HasSuffix(lines[1], "Aug 22") {
		t.Fatalf("narrow row = %#v, want truncated subject and full date", lines)
	}
}

func TestRowsRightAlignMixedDateAndTimeColumn(t *testing.T) {
	rows := testThreads(2)
	today := time.Now().Local()
	rows[0].Messages[0].InternalDate = time.Date(today.Year(), today.Month(), today.Day(), 12, 34, 0, 0, time.Local).UnixMilli()
	rows[1].Messages[0].InternalDate = today.AddDate(0, 0, -1).UnixMilli()
	model, _ := newTestApp(rows)
	const width = 50

	lines := strings.Split(ansi.Strip(model.list.rowsView(width, nil, 6)), "\n")
	for _, want := range []string{"12:34", today.AddDate(0, 0, -1).Format("Jan 02")} {
		var line string
		for _, candidate := range lines {
			if strings.HasSuffix(candidate, want) {
				line = candidate
				break
			}
		}
		if line == "" || lipgloss.Width(line) != width-1 {
			t.Fatalf("date %q line = %q, want right-aligned in column %d", want, line, width-1)
		}
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
	for _, want := range []string{"provision:", "read-only", "gmail.readonly"} {
		if !strings.Contains(model.status, want) {
			t.Fatalf("status = %q, want broker provisioning hint to contain %q", model.status, want)
		}
	}
	if cmd != nil {
		t.Fatal("error handling returned a quit command")
	}
}
