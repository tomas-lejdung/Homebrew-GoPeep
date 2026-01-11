package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// TUI Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("10"))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	urlStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("13"))

	viewerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	// Keybind styles
	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")) // Cyan for keys

	keySepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")) // Dim separator

	toggleActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10")) // Green for active toggles

	toggleInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("8")) // Dim for inactive toggles

	// Box styles for columns
	activeBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("12")).
			Padding(0, 1)

	inactiveBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)

	boxTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12"))

	boxTitleDimStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("8"))
)
