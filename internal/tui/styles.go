
package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Color palette
	Primary   = lipgloss.Color("#7C3AED") // Purple
	Success   = lipgloss.Color("#10B981") // Green
	Warning   = lipgloss.Color("#F59E0B") // Amber
	Danger    = lipgloss.Color("#EF4444") // Red
	Info      = lipgloss.Color("#3B82F6") // Blue
	Muted     = lipgloss.Color("#6B7280") // Gray
	BgDark    = lipgloss.Color("#1F2937")
	TextLight = lipgloss.Color("#F9FAFB")

	// Base styles
	StyleApp = lipgloss.NewStyle().
			Padding(0, 1)

	StyleTitle = lipgloss.NewStyle().
			Foreground(Primary).
			Bold(true)

	StyleSubtitle = lipgloss.NewStyle().
			Foreground(Muted)

	StyleNav = lipgloss.NewStyle().
			Padding(0, 1)

	StyleNavActive = lipgloss.NewStyle().
			Foreground(TextLight).
			Background(Primary).
			Padding(0, 2).
			Bold(true)

	StyleNavInactive = lipgloss.NewStyle().
				Foreground(Muted).
				Padding(0, 2)

	StyleHelp = lipgloss.NewStyle().
			Foreground(Muted).
			Padding(0, 1)

	StyleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Muted)

	StyleSuccess = lipgloss.NewStyle().Foreground(Success)
	StyleWarning = lipgloss.NewStyle().Foreground(Warning)
	StyleDanger  = lipgloss.NewStyle().Foreground(Danger)
	StyleInfo    = lipgloss.NewStyle().Foreground(Info)
	StyleMuted   = lipgloss.NewStyle().Foreground(Muted)

	// Status icons
	IconOK      = StyleSuccess.Render("✓")
	IconRunning = StyleWarning.Render("◎")
	IconWaiting = StyleMuted.Render("○")
	IconFailed  = StyleDanger.Render("✗")
	IconDelta   = StyleInfo.Render("Δ")
)


func RenderNav(pages []string, active int, width int) string {
	var items []string
	for i, p := range pages {
		if i == active {
			items = append(items, StyleNavActive.Render(p))
		} else {
			items = append(items, StyleNavInactive.Render(p))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, items...)
}


func RenderHelp(keys string) string {
	return StyleHelp.Render(keys)
}


func RenderProgress(done, total int, width int) string {
	if total == 0 {
		return ""
	}
	barWidth := width - 10
	if barWidth < 10 {
		barWidth = 10
	}
	filled := int(float64(done) / float64(total) * float64(barWidth))
	bar := ""
	for i := 0; i < barWidth; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	pct := float64(done) / float64(total) * 100
	return StyleInfo.Render(bar) + StyleMuted.Render("  "+pctStr(pct))
}

func pctStr(pct float64) string {
	if pct >= 100 {
		return "100%"
	}
	return fmt.Sprintf("%.0f%%", pct)
}
