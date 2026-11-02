package tui

import "github.com/charmbracelet/lipgloss"

// Theme color definitions
var (
	ColorPrimary   = lipgloss.Color("#00f0ff") // Cyber cyan
	ColorSecondary = lipgloss.Color("#ff007f") // Neon magenta
	ColorGreen     = lipgloss.Color("#00ff88") // Terminal green
	ColorAmber     = lipgloss.Color("#ffb800") // Amber
	ColorRed       = lipgloss.Color("#ff3366") // Bright red
	ColorMuted     = lipgloss.Color("#64748b") // Slate gray
	ColorLightText = lipgloss.Color("#f8fafc") // Almost white
	ColorDimText   = lipgloss.Color("#94a3b8") // Dim slate
	ColorBgDark    = lipgloss.Color("#090d16") // Deep terminal black
	ColorBgCard    = lipgloss.Color("#0f172a") // Card background
	ColorBorder    = lipgloss.Color("#1e293b") // Subtle border
	ColorHighlight = lipgloss.Color("#38bdf8") // Active highlight
)

// Reusable lipgloss styles
var (
	// App container
	AppContainerStyle = lipgloss.NewStyle().
				Padding(0, 1)

	// Header Styles
	HeaderLogoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ffffff")).
			Background(lipgloss.Color("#0284c7")).
			Padding(0, 1)

	HeaderVersionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#00f0ff")).
				Background(lipgloss.Color("#0f172a")).
				Padding(0, 1)

	HeaderSubtitleStyle = lipgloss.NewStyle().
				Foreground(ColorDimText).
				PaddingLeft(1)

	HeaderStatsStyle = lipgloss.NewStyle().
				Foreground(ColorLightText).
				Bold(true)

	HeaderMockBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#000000")).
				Background(ColorAmber).
				Padding(0, 1)

	// Box / Card Containers
	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1)

	BoxFocusedStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorHighlight).
			Padding(0, 1)

	BoxTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Padding(0, 0, 1, 0)

	// Machine status
	OnlineBadge = lipgloss.NewStyle().
			Foreground(ColorGreen).
			Bold(true).
			Render("● ONLINE")

	OfflineBadge = lipgloss.NewStyle().
			Foreground(ColorRed).
			Bold(true).
			Render("○ OFFLINE")

	// Engine Badges
	AgyBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ffffff")).
			Background(ColorSecondary).
			Padding(0, 1).
			Render("agy")

	OpenCodeBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(ColorAmber).
			Padding(0, 1).
			Render("OpenCode")

	// Status Badges
	StatusRunningBadge = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorGreen).
				Render("RUNNING")

	StatusDoneBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Render("DONE")

	StatusFailedBadge = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorRed).
				Render("FAILED")

	// List item styles
	ListItemSelected = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorLightText).
				Background(lipgloss.Color("#1e293b")).
				Padding(0, 1)

	ListItemNormal = lipgloss.NewStyle().
			Foreground(ColorDimText).
			Padding(0, 1)

	// Footer Styles
	FooterKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary)

	FooterDescStyle = lipgloss.NewStyle().
			Foreground(ColorDimText)

	FooterContainerStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Foreground(ColorDimText)
)
