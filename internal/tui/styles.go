package tui

import "github.com/charmbracelet/lipgloss"

// Base palette — Rose Pine Moon (neutral, terminal-safe)
var (
	colorBase    = lipgloss.Color("#232136")
	colorSurface = lipgloss.Color("#2a273f")
	colorMuted   = lipgloss.Color("#6e6a86")
	colorSubtle  = lipgloss.Color("#908caa")
	colorText    = lipgloss.Color("#e0def4")
	colorLove    = lipgloss.Color("#eb6f92") // errors
	colorGold    = lipgloss.Color("#f6c177") // warnings / loading
	colorRose    = lipgloss.Color("#ea9a97") // selected / accent
	colorFoam    = lipgloss.Color("#9ccfd8") // info / timestamps
	colorIris    = lipgloss.Color("#c4a7e7") // title / keys
	colorPine    = lipgloss.Color("#3e8fb0") // secondary accent
)

// Shell styles
var (
	appStyle      = lipgloss.NewStyle().Padding(1, 2)
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorIris)
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorIris)
	heroStyle     = lipgloss.NewStyle().Foreground(colorPine).Bold(true)
	helpStyle     = lipgloss.NewStyle().Foreground(colorMuted)
	metaStyle     = lipgloss.NewStyle().Foreground(colorSubtle)
	statusStyle   = lipgloss.NewStyle().Foreground(colorFoam)
	errorStyle    = lipgloss.NewStyle().Foreground(colorLove).Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(colorBase).Background(colorRose).Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(colorMuted)
	scrollStyle   = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	deletedStyle  = lipgloss.NewStyle().Strikethrough(true).Foreground(colorMuted)
)

// Tab styles
var (
	tabStyle = lipgloss.NewStyle().
			Foreground(colorSubtle).
			Padding(0, 2)

	activeTabStyle = lipgloss.NewStyle().
			Foreground(colorBase).
			Background(colorIris).
			Bold(true).
			Padding(0, 2)
)

// Layout panes
var (
	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSurface).
			Padding(0, 1)

	activePaneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorIris).
			Padding(0, 1)
)

// Panel / content borders — used only for detail views, not lists
var (
	panelStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorSurface).Padding(0, 1)
	contentStyle = lipgloss.NewStyle().Padding(1)
)

// Stats card
var (
	statCardStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPine).Padding(0, 2)
	statNumberStyle = lipgloss.NewStyle().Bold(true).Foreground(colorRose)
	statLabelStyle  = lipgloss.NewStyle().Foreground(colorSubtle)
)

// Filter input bar
var filterBarStyle = lipgloss.NewStyle().Foreground(colorGold).Bold(true)

// Confirm dialog
var (
	confirmBoxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorLove).Padding(1, 2)
	confirmWarningStyle = lipgloss.NewStyle().Foreground(colorLove).Bold(true)
	settingsBoxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorIris).Padding(1, 2)
)

// agentBadgeColors maps agent type names to terminal-safe accent colors.
var agentBadgeColors = map[string]lipgloss.Color{
	"grep":     colorFoam,
	"explore":  colorPine,
	"executor": colorGold,
	"designer": colorIris,
	"general":  colorSubtle,
	"triage":   colorLove,
	"web":      colorRose,
}

// agentColor returns the accent assigned to an agent name, falling back to a
// neutral tone for agents outside the known set.
func agentColor(agent string) lipgloss.Color {
	if color, ok := agentBadgeColors[agent]; ok {
		return color
	}
	return colorSubtle
}

// agentBadge renders a colored [agent] pill for a given agent type string.
func agentBadge(agent string) string {
	return lipgloss.NewStyle().Foreground(agentColor(agent)).Bold(true).Render("[" + agent + "]")
}
