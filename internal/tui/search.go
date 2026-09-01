package tui

import tea "github.com/charmbracelet/bubbletea"

func (m app) searchScreen() string {
	return m.list.View(m.account, m.layout.width, m.layout.searchListHeight, m.ctx.labelNameByID, m.usesEnvToken(), m.activeFilterName()) + "\n" + m.search.View() + "\n" + m.statusView()
}

func (m app) updateSearchKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.search.Blur()
		m.view = listView
		command := m.requestPreview()
		return m, command
	case "enter":
		m.list.query = m.search.Value()
		m.search.Blur()
		m.view = listView
		cmd := m.refreshListing()
		return m, cmd
	}
	var command tea.Cmd
	m.search, command = m.search.Update(message)
	return m, command
}
