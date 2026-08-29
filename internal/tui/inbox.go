package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

type inboxModel struct {
	rows     []*gmail.Thread
	cursor   int
	selected map[string]struct{}
	query    string
}

func newInboxModel() inboxModel {
	return inboxModel{selected: make(map[string]struct{})}
}

func (m *inboxModel) setRows(rows []*gmail.Thread) {
	m.rows = rows
	m.cursor = 0
	m.selected = make(map[string]struct{})
}

func (m *inboxModel) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.rows)-1, m.cursor+delta))
}

func (m *inboxModel) toggleSelected() {
	if len(m.rows) == 0 {
		return
	}
	id := m.rows[m.cursor].ID
	if _, selected := m.selected[id]; selected {
		delete(m.selected, id)
		return
	}
	m.selected[id] = struct{}{}
}

func (m inboxModel) targetIDs() []string {
	if len(m.rows) == 0 {
		return nil
	}
	if len(m.selected) == 0 {
		return []string{m.rows[m.cursor].ID}
	}
	ids := make([]string, 0, len(m.selected))
	for _, row := range m.rows {
		if _, selected := m.selected[row.ID]; selected {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

func (m *inboxModel) remove(ids []string) int {
	removed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		removed[id] = struct{}{}
	}
	firstRemoved := len(m.rows)
	rows := make([]*gmail.Thread, 0, len(m.rows)-len(ids))
	for index, row := range m.rows {
		if _, remove := removed[row.ID]; remove {
			if firstRemoved == len(m.rows) {
				firstRemoved = index
			}
			delete(m.selected, row.ID)
			continue
		}
		rows = append(rows, row)
	}
	m.rows = rows
	if len(m.rows) == 0 {
		m.cursor = 0
	} else {
		m.cursor = min(m.cursor, len(m.rows)-1)
	}
	return firstRemoved
}

func (m *inboxModel) updateLabels(ids, add, remove []string) {
	targets := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		targets[id] = struct{}{}
	}
	for _, thread := range m.rows {
		if _, target := targets[thread.ID]; !target {
			continue
		}
		for _, message := range thread.Messages {
			message.LabelIDs = withLabels(message.LabelIDs, add, remove)
		}
	}
}

func (m inboxModel) View(account string, width, height int, labelNameByID map[string]string, envToken bool) string {
	lines := []string{
		titleStyle.Render(m.title(account, envToken)),
		helpStyle.Render("j/k move · space select · e archive · d trash · u unread · l label · / search · tab account · enter read · R refresh · q quit"),
		m.rowsView(width, labelNameByID, height-3),
	}
	return strings.Join(lines, "\n")
}

func (m app) inboxView() string {
	if !m.previewEnabled() {
		return m.list.View(m.account, m.layout.width, m.layout.height, m.ctx.labelNameByID, m.usesEnvToken()) + "\n" + m.statusView()
	}
	listPane := paneStyle.Width(m.layout.listPaneWidth).Height(m.layout.splitPaneHeight).Render(m.list.rowsView(m.layout.listContentWidth, m.ctx.labelNameByID, m.layout.splitContentHeight))
	previewPane := paneStyle.Width(m.layout.previewPaneWidth).Height(m.layout.splitPaneHeight).Render(m.previewView(m.layout.previewContentWidth, m.layout.splitContentHeight))
	return titleStyle.Render(m.list.title(m.account, m.usesEnvToken())) + "\n" +
		helpStyle.Render("j/k move · space select · enter read · e archive · d trash · u unread · l label · / search · tab account · R refresh · q quit") + "\n" +
		lipgloss.JoinHorizontal(lipgloss.Top, listPane, previewPane) + "\n" +
		m.statusView()
}

func (m inboxModel) title(account string, envToken bool) string {
	account = render.SanitizeTerminal(account)
	title := fmt.Sprintf("Mailbox — %s inbox", account)
	if m.query != "" {
		title = fmt.Sprintf("Mailbox — %s search: %s", account, m.query)
	}
	if envToken {
		return title + " [pinned]"
	}
	return title
}

func (m inboxModel) rowsView(width int, labelNameByID map[string]string, height int) string {
	return m.rowsViewAt(width, labelNameByID, height, time.Now().Local())
}

func (m inboxModel) rowsViewAt(width int, labelNameByID map[string]string, height int, now time.Time) string {
	if len(m.rows) == 0 {
		return "No threads."
	}
	visible := max(1, height/3)
	start := min(max(0, m.cursor-visible+1), max(0, len(m.rows)-visible))
	end := min(len(m.rows), start+visible)
	innerWidth := max(1, width-1)
	const dateColumnWidth = 6
	lines := make([]string, 0, (end-start)*2)
	for index := start; index < end; index++ {
		thread := m.rows[index]
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		selection := " "
		if _, selected := m.selected[thread.ID]; selected {
			selection = "*"
		}
		from, subject, date := metadataAt(thread, now)
		prefix := cursor + selection + " "
		first := prefix + truncateSender(from, max(0, innerWidth-lipgloss.Width(prefix)))
		indent := "     "
		if selection == "*" {
			indent = "*    "
		}
		leftWidth := innerWidth - dateColumnWidth - 1
		var second string
		if leftWidth <= 0 {
			second = strings.Repeat(" ", max(0, innerWidth-lipgloss.Width(date))) + date
		} else {
			left := truncate(indent+subject+labelChips(thread, labelNameByID), leftWidth)
			date = strings.Repeat(" ", max(0, dateColumnWidth-lipgloss.Width(date))) + date
			second = padDisplay(left, leftWidth) + " " + date
		}
		if threadUnread(thread) {
			first = unreadStyle.Render(first)
		}
		second = subjectStyle.Render(second)
		if selection == "*" {
			first = selectedStyle.Render(first)
			second = selectedStyle.Render(second)
		}
		lines = append(lines, first, second)
		if index+1 < end {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

func labelNames(labels []gmail.Label) map[string]string {
	names := make(map[string]string, len(labels))
	for _, label := range labels {
		if label.Type == "user" {
			names[label.ID] = render.SanitizeTerminal(label.Name)
		}
	}
	return names
}

func labelChips(thread *gmail.Thread, labelNameByID map[string]string) string {
	message := gmail.LatestMessage(thread)
	if message == nil {
		return ""
	}
	var chips []string
	for _, label := range message.LabelIDs {
		if name := labelNameByID[label]; name != "" {
			chips = append(chips, name)
		}
	}
	if len(chips) == 0 {
		return ""
	}
	return " [" + strings.Join(chips, "] [") + "]"
}

func (m app) updateListKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.unlocking {
		m.deflectUnlock()
		return m, nil
	}
	switch message.String() {
	case keyDown, "down":
		m.list.move(1)
		command := m.requestPreview()
		return m, command
	case keyUp, "up":
		m.list.move(-1)
		command := m.requestPreview()
		return m, command
	case " ":
		m.list.toggleSelected()
	case keyArchive:
		return m.startAction("archive", m.list.targetIDs(), nil, []string{"INBOX"}, false)
	case keyTrash:
		return m.startAction("trash", m.list.targetIDs(), nil, nil, false)
	case keyUnread:
		ids := m.list.targetIDs()
		if len(ids) == 0 {
			return m, nil
		}
		if threadUnread(m.list.rows[m.list.cursor]) {
			return m.startAction("mark", ids, nil, []string{"UNREAD"}, false)
		}
		return m.startAction("mark", ids, []string{"UNREAD"}, nil, false)
	case keyLabel:
		m.view = labelPickerView
		m.label.SetValue("")
		m.labelCursor = 0
		focus := m.label.Focus()
		if m.ctx.labels == nil {
			m.loading = true
			labels := listLabelsCmd(m.beginRequest(labelOperation))
			return m, tea.Batch(focus, labels, m.spinnerCmd())
		}
		return m, focus
	case keySearch:
		m.view = searchView
		m.search.SetValue("")
		return m, m.search.Focus()
	case "tab":
		return m.switchAccount()
	case "enter":
		if len(m.list.rows) == 0 {
			return m, nil
		}
		m.loading = true
		request := m.beginRequest(threadOperation)
		return m, m.loadingCmd(getThreadCmd(request, m.list.rows[m.list.cursor].ID))
	case keyRefresh:
		m.loading = true
		request := m.beginRequest(listOperation)
		return m, m.loadingCmd(listThreadsCmd(request, m.list.query))
	case keyQuit:
		if m.deflectUnlock() {
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m app) startAction(action string, ids, add, remove []string, advance bool) (tea.Model, tea.Cmd) {
	if m.unlocking {
		m.deflectUnlock()
		return m, nil
	}
	if len(ids) == 0 {
		return m, nil
	}
	if m.pending != nil {
		return m, nil
	}
	m.pending = &pendingAction{action: action, ids: ids, add: add, remove: remove, advance: advance}
	if !m.ctx.writeReady() {
		return m.startUnlock()
	}
	return m.dispatchPending()
}

func metadata(thread *gmail.Thread) (from, subject, date string) {
	return metadataAt(thread, time.Now().Local())
}

func metadataAt(thread *gmail.Thread, now time.Time) (from, subject, date string) {
	message := gmail.LatestMessage(thread)
	if message == nil {
		return "", "", ""
	}
	date = formatInboxDate(time.UnixMilli(message.InternalDate).Local(), now)
	return render.SanitizeTerminal(gmail.Sender(message.Header("From"))), render.SanitizeTerminal(message.Header("Subject")), date
}

func formatInboxDate(dateTime, now time.Time) string {
	dateTime = dateTime.Local()
	now = now.Local()
	if dateTime.Year() == now.Year() && dateTime.Month() == now.Month() && dateTime.Day() == now.Day() {
		return dateTime.Format("15:04")
	}
	return dateTime.Format("Jan 02")
}

func threadUnread(thread *gmail.Thread) bool {
	for _, message := range thread.Messages {
		if message.HasLabel("UNREAD") {
			return true
		}
	}
	return false
}

func threadHasLabel(thread *gmail.Thread, labelID string) bool {
	for _, message := range thread.Messages {
		if message.HasLabel(labelID) {
			return true
		}
	}
	return false
}

func withLabels(labels, add, remove []string) []string {
	values := make(map[string]struct{}, len(labels)+len(add))
	for _, label := range labels {
		values[label] = struct{}{}
	}
	for _, label := range remove {
		delete(values, label)
	}
	for _, label := range add {
		values[label] = struct{}{}
	}
	updated := make([]string, 0, len(values))
	for _, label := range labels {
		if _, exists := values[label]; exists {
			updated = append(updated, label)
			delete(values, label)
		}
	}
	for _, label := range add {
		if _, exists := values[label]; exists {
			updated = append(updated, label)
			delete(values, label)
		}
	}
	return updated
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	const ellipsis = "…"
	if width <= lipgloss.Width(ellipsis) {
		return ellipsis
	}
	limit := width - lipgloss.Width(ellipsis)
	var output strings.Builder
	used := 0
	for graphemes := uniseg.NewGraphemes(value); graphemes.Next(); {
		cluster := graphemes.Str()
		clusterWidth := lipgloss.Width(cluster)
		if used+clusterWidth > limit {
			break
		}
		output.WriteString(cluster)
		used += clusterWidth
	}
	return output.String() + ellipsis
}

func truncateSender(sender string, width int) string {
	if lipgloss.Width(sender) <= width {
		return sender
	}
	if split := strings.LastIndex(sender, " <"); split > 0 && strings.HasSuffix(sender, ">") {
		name := sender[:split]
		address := sender[split+2 : len(sender)-1]
		addressWidth := width - lipgloss.Width(name+" <>")
		if addressWidth > lipgloss.Width("…") {
			return name + " <" + middleTruncate(address, addressWidth) + ">"
		}
		return truncate(name, width)
	}
	return truncate(sender, width)
}

func middleTruncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	const ellipsis = "…"
	if width <= lipgloss.Width(ellipsis) {
		return ellipsis
	}
	limit := width - lipgloss.Width(ellipsis)
	headLimit := (limit + 2) / 2
	tailLimit := limit - headLimit
	clusters := make([]string, 0, len(value))
	for graphemes := uniseg.NewGraphemes(value); graphemes.Next(); {
		clusters = append(clusters, graphemes.Str())
	}
	var head strings.Builder
	headWidth := 0
	for _, cluster := range clusters {
		clusterWidth := lipgloss.Width(cluster)
		if headWidth+clusterWidth > headLimit {
			break
		}
		head.WriteString(cluster)
		headWidth += clusterWidth
	}
	tailClusters := make([]string, 0, len(clusters))
	tailWidth := 0
	for index := len(clusters) - 1; index >= 0; index-- {
		cluster := clusters[index]
		clusterWidth := lipgloss.Width(cluster)
		if tailWidth+clusterWidth > tailLimit {
			break
		}
		tailClusters = append(tailClusters, cluster)
		tailWidth += clusterWidth
	}
	var tail strings.Builder
	for index := len(tailClusters) - 1; index >= 0; index-- {
		tail.WriteString(tailClusters[index])
	}
	return head.String() + ellipsis + tail.String()
}

func padDisplay(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}
