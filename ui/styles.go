package ui

import "github.com/charmbracelet/lipgloss"

// lipglossStyle is an alias so the rest of the package can name a style without
// importing lipgloss into every file.
type lipglossStyle = lipgloss.Style

// Colors are adaptive so the dashboard stays readable on light and dark
// terminals without a config file.
var (
	colorDim     = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C7086"}
	colorText    = lipgloss.AdaptiveColor{Light: "#1F1F1F", Dark: "#CDD6F4"}
	colorGreen   = lipgloss.AdaptiveColor{Light: "#1B7F3B", Dark: "#A6E3A1"}
	colorCyan    = lipgloss.AdaptiveColor{Light: "#0D6E8C", Dark: "#89DCEB"}
	colorYellow  = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#F9E2AF"}
	colorRed     = lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#F38BA8"}
	colorMagenta = lipgloss.AdaptiveColor{Light: "#8250DF", Dark: "#CBA6F7"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#0057B7", Dark: "#89B4FA"}
)

var (
	sDim      = lipgloss.NewStyle().Foreground(colorDim)
	sText     = lipgloss.NewStyle().Foreground(colorText)
	sBold     = lipgloss.NewStyle().Bold(true)
	sHeader   = lipgloss.NewStyle().Foreground(colorDim).Bold(true)
	sGreen    = lipgloss.NewStyle().Foreground(colorGreen)
	sCyan     = lipgloss.NewStyle().Foreground(colorCyan)
	sYellow   = lipgloss.NewStyle().Foreground(colorYellow)
	sRed      = lipgloss.NewStyle().Foreground(colorRed)
	sMagenta  = lipgloss.NewStyle().Foreground(colorMagenta)
	sAccent   = lipgloss.NewStyle().Foreground(colorAccent)
	sSelected = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	sSection  = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
)

// stageStyle colors a stage glyph by its step status.
func stageStyle(status string) lipgloss.Style {
	switch status {
	case "completed":
		return sGreen
	case "running":
		return sCyan
	case "fixing":
		return sMagenta
	case "awaiting_approval", "fix_review":
		return sYellow
	case "failed":
		return sRed
	default:
		return sDim
	}
}

// prStyle colors the PR column by pull-request state.
func prStyle(state string) lipgloss.Style {
	switch state {
	case "open":
		return sGreen
	case "merged":
		return sMagenta
	case "closed":
		return sRed
	case "draft":
		return sDim
	default:
		return sDim
	}
}

// runStyle colors a run's step label by how much it wants your attention.
func runStyle(status string, parked bool) lipgloss.Style {
	if parked {
		return sYellow
	}
	switch status {
	case "failed":
		return sRed
	case "running":
		return sCyan
	case "completed":
		return sGreen
	case "cancelled":
		return sDim
	default:
		return sText
	}
}
