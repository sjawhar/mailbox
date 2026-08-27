package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type previewModel struct {
	requestedID string
	content     string
	err         string
	loading     bool
	cache       map[string]string
}

func newPreviewModel() previewModel {
	return previewModel{cache: make(map[string]string)}
}

func (m app) previewView(width, height int) string {
	title := previewTitleStyle.Render("Preview")
	var content string
	switch {
	case m.preview.loading:
		content = m.spinner.View() + " Loading selected thread…"
	case m.preview.err != "":
		content = errorStyle.Render(m.preview.err)
	case m.preview.content == "":
		content = "Select a thread to preview it."
	default:
		content = m.preview.content
	}
	return title + "\n" + clampPreviewLines(content, width, max(1, height-1))
}

func clampPreviewLines(content string, width, height int) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "…")
	}
	if len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	const more = "… press enter to read the full thread"
	if height == 1 {
		return ansi.Truncate(helpStyle.Render(more), width, "…")
	}
	return strings.Join(lines[:height-1], "\n") + "\n" + ansi.Truncate(helpStyle.Render(more), width, "…")
}

func (m app) previewEnabled() bool {
	return m.view == listView && m.layout.width >= splitPreviewMinWidth
}

func (m app) previewWidth() int {
	return m.layout.previewContentWidth
}

func (m *app) requestPreview() tea.Cmd {
	request := m.beginRequest(previewOperation)
	if !m.previewEnabled() || len(m.list.rows) == 0 {
		m.preview.requestedID = ""
		m.preview.content = ""
		m.preview.err = ""
		m.preview.loading = false
		return nil
	}
	threadID := m.list.rows[m.list.cursor].ID
	m.preview.requestedID = threadID
	if content, cached := m.preview.cache[threadID]; cached {
		m.preview.content = content
		m.preview.err = ""
		m.preview.loading = false
		return nil
	}
	m.preview.content = ""
	m.preview.err = ""
	m.preview.loading = true
	return previewDebounceCmd(request, threadID)
}

func (m app) previewSelectionCurrent(threadID string) bool {
	return m.previewEnabled() &&
		m.preview.requestedID == threadID &&
		len(m.list.rows) > 0 &&
		m.list.rows[m.list.cursor].ID == threadID
}
