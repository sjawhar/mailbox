package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

type threadModel struct {
	thread            *gmail.Thread
	rendered          *render.RenderedThread
	keepQuotes        bool
	linkInput         string
	attachments       []render.Attachment
	attachmentCursor  int
	listingGeneration uint64
}

func (m *app) renderCurrentThread() error {
	rendered, err := render.RenderThread(m.thread.thread, render.Options{KeepQuotes: m.thread.keepQuotes})
	if err != nil {
		return err
	}
	contents, err := renderThreadDocument(rendered, m.viewport.Width)
	if err != nil {
		return err
	}
	m.thread.rendered = rendered
	m.viewport.SetContent(contents)
	m.viewport.GotoTop()
	return nil
}

func renderMarkdown(markdown string, width int) (string, error) {
	style := os.Getenv("GLAMOUR_STYLE")
	if style == "" {
		style = styles.DarkStyle
	}
	return render.RenderTerminalMarkdown(markdown, max(20, width), style)
}

func renderThreadDocument(thread *render.RenderedThread, width int) (string, error) {
	var document strings.Builder
	document.WriteString(titleStyle.Render(render.SanitizeTerminal(thread.Subject)))
	for index, message := range thread.Messages {
		if index > 0 {
			document.WriteString("\n")
			document.WriteString(helpStyle.Render(strings.Repeat("─", max(1, width))))
		}
		document.WriteString("\n\n")
		document.WriteString(messageHeaderStyle.Render(truncate(formatMessageHeader(message), width)))
		document.WriteString("\n")
		body, err := renderMarkdown(render.TerminalMarkdown(message.Markdown, message.Links), width)
		if err != nil {
			return "", err
		}
		document.WriteString(body)
		if len(message.Attachments) > 0 {
			attachments := make([]string, 0, len(message.Attachments))
			for _, attachment := range message.Attachments {
				attachments = append(attachments, fmt.Sprintf("[%d] %s", attachment.N, render.SanitizeTerminal(attachment.Filename)))
			}
			document.WriteString("\n")
			document.WriteString(helpStyle.Render("Attachments: " + strings.Join(attachments, ", ")))
		}
	}
	return document.String(), nil
}

func formatMessageHeader(message render.RenderedMessage) string {
	return fmt.Sprintf(
		"%s → %s · %s",
		gmail.Sender(render.SanitizeTerminal(message.From)),
		gmail.Sender(render.SanitizeTerminal(message.To)),
		message.Date.UTC().Format("2006-01-02 15:04 MST"),
	)
}

func renderPreview(thread *gmail.Thread, width int) (string, error) {
	rendered, err := render.RenderThread(thread, render.Options{})
	if err != nil {
		return "", err
	}
	return renderThreadDocument(rendered, width)
}

func (m app) updateThreadKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	value := message.String()
	if m.unlocking {
		return m, nil
	}
	if m.thread.linkInput != "" {
		switch value {
		case "esc":
			m.thread.linkInput = ""
			m.clearStatus()
			return m, nil
		case "enter":
			input := m.thread.linkInput
			m.thread.linkInput = ""
			if link, ok := m.thread.link(input); ok {
				m.clearStatus()
				request := m.beginRequest(openOperation)
				return m, openLinkCmd(request, link.URL)
			}
			m.status = fmt.Sprintf("link [%s] not found", input)
			m.statusError = false
			return m, nil
		default:
			if isLinkInputDigit(value) {
				m.thread.linkInput += value
				m.status = fmt.Sprintf("link number: %s · enter open · esc cancel", m.thread.linkInput)
				m.statusError = false
			}
			return m, nil
		}
	}

	switch value {
	case "esc":
		m.view = listView
		return m, nil
	case keyNext:
		if m.list.cursor+1 >= len(m.list.rows) {
			m.status = "no newer threads"
			m.statusError = false
			return m, nil
		}
		m.list.cursor++
		m.clearStatus()
		request := m.beginLoading(threadOperation)
		return m, m.loadingCmd(getThreadCmd(request, m.list.rows[m.list.cursor].ID))
	case keyPrevious:
		if m.list.cursor-1 < 0 {
			m.status = "no older threads"
			m.statusError = false
			return m, nil
		}
		m.list.cursor--
		m.clearStatus()
		request := m.beginLoading(threadOperation)
		return m, m.loadingCmd(getThreadCmd(request, m.list.rows[m.list.cursor].ID))
	case keyQuotes:
		m.thread.keepQuotes = !m.thread.keepQuotes
		if err := m.renderCurrentThread(); err != nil {
			m.surfaceError(err)
		}
		return m, nil
	case keyReply:
		return m.openReply()
	case keyCompose:
		return m.startCompose()
	case keyArchive:
		if !m.threadActionsCurrent() {
			return m, nil
		}
		return m.startAction("archive", []string{m.thread.thread.ID}, nil, []string{"INBOX"}, true)
	case keyTrash:
		if !m.threadActionsCurrent() {
			return m, nil
		}
		return m.startAction("trash", []string{m.thread.thread.ID}, nil, nil, true)
	case "d":
		// Unbound as of v2.1 (# trashes). Consumed here so the key never
		// reaches the viewport, whose default keymap scrolls on d.
		return m, nil
	case keyAttachments:
		attachments, err := render.ThreadAttachments(m.thread.thread)
		if err != nil {
			m.surfaceError(err)
			return m, nil
		}
		if len(attachments) == 0 {
			m.status = "thread has no attachments"
			m.statusError = false
			return m, nil
		}
		m.thread.attachments = attachments
		m.thread.attachmentCursor = 0
		m.view = attachmentPickerView
		return m, nil
	case keyOpenHTML:
		request := m.beginLoading(openOperation)
		return m, m.loadingCmd(openHTMLCmd(request, m.thread.thread))
	}
	if isLinkFirstDigit(value) && m.thread.rendered != nil {
		if len(m.thread.rendered.AllLinks()) > 9 {
			m.thread.linkInput = value
			m.status = fmt.Sprintf("link number: %s · enter open · esc cancel", value)
			m.statusError = false
			return m, nil
		}
		if link, ok := m.thread.link(value); ok {
			request := m.beginRequest(openOperation)
			return m, openLinkCmd(request, link.URL)
		}
	}
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(message)
	return m, command
}

func (m app) threadActionsCurrent() bool {
	return m.thread.thread != nil && m.currentRows(m.thread.listingGeneration)
}

func (m app) threadView() string {
	return titleStyle.Render("Thread") + "\n" + m.viewport.View() + "\n" + helpStyle.Render("r reply · c compose · n/p threads · j/k scroll · esc back") + "\n" + m.statusView()
}

func (m app) attachmentPickerView() string {
	lines := []string{titleStyle.Render("Attachments"), m.viewport.View()}
	for index, attachment := range m.thread.attachments {
		cursor := " "
		if index == m.thread.attachmentCursor {
			cursor = ">"
		}
		lines = append(lines, fmt.Sprintf("%s [%d] %s (%s, %d bytes)", cursor, attachment.N, render.SanitizeTerminal(attachment.Filename), render.SanitizeTerminal(attachment.MimeType), attachment.Size))
	}
	lines = append(lines, helpStyle.Render("j/k move · enter download to current directory · esc back"), m.statusView())
	return strings.Join(lines, "\n")
}

func (m app) updateAttachmentKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.view = threadView
		return m, nil
	case keyDown, "down":
		m.thread.attachmentCursor = min(len(m.thread.attachments)-1, m.thread.attachmentCursor+1)
		return m, nil
	case keyUp, "up":
		m.thread.attachmentCursor = max(0, m.thread.attachmentCursor-1)
		return m, nil
	case "enter":
		request := m.beginLoading(attachmentOperation)
		return m, m.loadingCmd(saveAttachmentCmd(request, m.thread.attachments[m.thread.attachmentCursor]))
	}
	return m, nil
}

func (m app) labelPickerView() string {
	labels := m.filteredLabels()
	lines := []string{titleStyle.Render("Labels"), m.label.View()}
	ids := m.list.targetIDs()
	for index, label := range labels {
		cursor := " "
		if index == m.labelCursor {
			cursor = ">"
		}
		marker := " "
		if m.allThreadsHaveLabel(ids, label.ID) {
			marker = "*"
		}
		lines = append(lines, fmt.Sprintf("%s%s %s", cursor, marker, render.SanitizeTerminal(label.Name)))
	}
	if len(labels) == 0 {
		lines = append(lines, "No matching labels.")
	}
	lines = append(lines, helpStyle.Render("↑/↓ move · enter toggle · esc cancel"), m.statusView())
	return strings.Join(lines, "\n")
}

func (m app) updateLabelKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.label.Blur()
		m.view = listView
		return m, nil
	case "down":
		m.labelCursor = min(len(m.filteredLabels())-1, m.labelCursor+1)
		return m, nil
	case "up":
		m.labelCursor = max(0, m.labelCursor-1)
		return m, nil
	case "enter":
		labels := m.filteredLabels()
		if len(labels) == 0 {
			return m, nil
		}
		label := labels[m.labelCursor]
		ids := m.list.targetIDs()
		m.label.Blur()
		m.view = listView
		if m.allThreadsHaveLabel(ids, label.ID) {
			return m.startAction("label", ids, nil, []string{label.ID}, false)
		}
		return m.startAction("label", ids, []string{label.ID}, nil, false)
	}
	var command tea.Cmd
	m.label, command = m.label.Update(message)
	m.labelCursor = 0
	return m, command
}

func (m app) filteredLabels() []gmail.Label {
	filter := strings.ToLower(m.label.Value())
	labels := make([]gmail.Label, 0, len(m.ctx.labels))
	for _, label := range m.ctx.labels {
		if label.Type == "user" && strings.Contains(strings.ToLower(label.Name), filter) {
			labels = append(labels, label)
		}
	}
	return labels
}

func (m app) allThreadsHaveLabel(ids []string, labelID string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		for _, thread := range m.list.rows {
			if thread.ID == id && !threadHasLabel(thread, labelID) {
				return false
			}
		}
	}
	return true
}

func (m threadModel) link(value string) (render.Link, bool) {
	if value == "" || m.rendered == nil {
		return render.Link{}, false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return render.Link{}, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return render.Link{}, false
	}
	for _, link := range m.rendered.AllLinks() {
		if link.N == number {
			return link, true
		}
	}
	return render.Link{}, false
}

func isLinkFirstDigit(value string) bool {
	return len(value) == 1 && value[0] >= '1' && value[0] <= '9'
}

func isLinkInputDigit(value string) bool {
	return len(value) == 1 && value[0] >= '0' && value[0] <= '9'
}
