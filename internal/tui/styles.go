package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	unreadStyle = lipgloss.NewStyle().
			Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	paneStyle     = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
	previewTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	messageHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75"))
	subjectStyle       = lipgloss.NewStyle().Faint(true)
)
