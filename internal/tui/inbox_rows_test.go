package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

	message := runCmd(t, listThreadsCmd(model.currentRequest(listOperation), "", nil))
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
	newerTime := time.Date(2000, time.August, 26, 12, 0, 0, 0, time.Local)
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
	if from != "Example Commenter (via Google Docs) <comments-noreply@docs.google.com>" || subject != "Résumé for José" || date != "Aug 26" {
		t.Fatalf("metadata() = (%q, %q, %q), want newest decoded message", from, subject, date)
	}
}

func TestFormatInboxDateUsesLocalCalendarDay(t *testing.T) {
	tests := []struct {
		name string
		date time.Time
		now  time.Time
		want string
	}{
		{
			name: "midnight on same day",
			date: time.Date(2026, time.August, 29, 0, 0, 0, 0, time.Local),
			now:  time.Date(2026, time.August, 29, 0, 0, 0, 0, time.Local),
			want: "00:00",
		},
		{
			name: "23:59 on same day",
			date: time.Date(2026, time.August, 29, 23, 59, 0, 0, time.Local),
			now:  time.Date(2026, time.August, 29, 23, 59, 0, 0, time.Local),
			want: "23:59",
		},
		{
			name: "previous day at midnight",
			date: time.Date(2026, time.August, 29, 23, 59, 0, 0, time.Local),
			now:  time.Date(2026, time.August, 30, 0, 0, 0, 0, time.Local),
			want: "Aug 29",
		},
		{
			name: "midnight remains today at 23:59",
			date: time.Date(2026, time.August, 30, 0, 0, 0, 0, time.Local),
			now:  time.Date(2026, time.August, 30, 23, 59, 0, 0, time.Local),
			want: "00:00",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatInboxDate(test.date, test.now); got != test.want {
				t.Fatalf("formatInboxDate(%s, %s) = %q, want %q", test.date, test.now, got, test.want)
			}
		})
	}
}

func TestRowsRenderSenderAndSubjectOnSeparateLines(t *testing.T) {
	rows := testThreads(1)
	now := time.Date(2026, time.August, 29, 23, 59, 0, 0, time.Local)
	rows[0].Messages[0].InternalDate = time.Date(2026, time.August, 29, 12, 34, 0, 0, time.Local).UnixMilli()
	rows[0].Messages[0].LabelIDs = append(rows[0].Messages[0].LabelIDs, "STARRED", "Label_7")
	model, _ := newTestApp(rows)
	_, _, date := metadataAt(rows[0], now)
	rendered := ansi.Strip(model.list.rowsViewAt(40, nil, 2, now))
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
	now := time.Date(2026, time.August, 29, 23, 59, 0, 0, time.Local)
	rows[0].Messages[0].InternalDate = time.Date(2026, time.August, 29, 12, 34, 0, 0, time.Local).UnixMilli()
	rows[1].Messages[0].InternalDate = time.Date(2026, time.August, 28, 12, 34, 0, 0, time.Local).UnixMilli()
	model, _ := newTestApp(rows)
	const width = 50

	lines := strings.Split(ansi.Strip(model.list.rowsViewAt(width, nil, 6, now)), "\n")
	if len(lines) != 5 {
		t.Fatalf("rows = %#v, want two rows with one separator", lines)
	}
	for index, want := range []string{"12:34", "Aug 28"} {
		sender, subject := lines[index*3], lines[index*3+1]
		if strings.Contains(sender, want) || !strings.HasSuffix(subject, want) || lipgloss.Width(subject) != width-1 {
			t.Fatalf("row %d = (sender=%q subject=%q), want date %q only at the subject line's right edge", index, sender, subject, want)
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
	err := &gmail.APIError{Status: 403, Reason: "insufficientPermissions", Message: "scope missing"}

	model, cmd := update(t, model, errMsg{request: model.currentRequest(listOperation), err: err})
	for _, want := range []string{"provision:", "accounts.work.read_credential_env", "gmail.readonly"} {
		if !strings.Contains(model.status, want) {
			t.Fatalf("status = %q, want provisioning hint to contain %q", model.status, want)
		}
	}
	if cmd != nil {
		t.Fatal("error handling returned a quit command")
	}
}
