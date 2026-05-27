// Package components — roulettehistory.go renders the iconic
// last-N-winners strip + an optional hot/cold stats panel for the
// roulette screen. The strip is always visible; the stats panel toggles
// via the 's' hotkey and is composed on top of the dimmed cabinet body
// via the existing Overlay helper.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
	roulettemp "github.com/nickna/ssh.night.ms/internal/doors/roulette/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// RenderRouletteHistory paints the last-N strip as a row of small colored
// tiles. items is expected to be oldest-first so the most recent winner
// renders rightmost — matching the convention on real casino history
// boards.
func RenderRouletteHistory(items []roulette.Pocket) string {
	if len(items) == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorDim)).
			Italic(true).
			Render("HISTORY:  (no spins yet)")
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorAccentDim)).
		Render("HISTORY: "))
	for _, p := range items {
		b.WriteString(renderHistoryTile(p))
		b.WriteString(" ")
	}
	return b.String()
}

// renderHistoryTile draws one tile: "[NN]" colored by pocket color. Green
// for zeros, red for red pockets, dim-text for black so they read as
// alternating dots even at a glance.
func renderHistoryTile(p roulette.Pocket) string {
	fg := theme.ColorText
	bg := theme.ColorSurfaceAlt
	switch p.Color() {
	case roulette.Red:
		fg = theme.ColorBackground
		bg = theme.ColorRed
	case roulette.Black:
		fg = theme.ColorText
		bg = theme.ColorMuted
	case roulette.Green:
		fg = theme.ColorBackground
		bg = theme.ColorGreen
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(fg)).
		Background(lipgloss.Color(bg)).
		Bold(true).
		Padding(0, 1).
		Render(p.Number())
}

// RenderRouletteStats produces a bordered modal listing the hot 5 / cold 5
// numbers plus longest red / black streaks. Caller wraps via the existing
// Overlay helper to composite on top of the cabinet view.
func RenderRouletteStats(stats roulettemp.Stats) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(lipgloss.Color(theme.ColorAccent)).
		Background(lipgloss.Color(theme.ColorSurface)).
		Foreground(lipgloss.Color(theme.ColorText)).
		Padding(1, 3)

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).
		Render("ROULETTE STATS")
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))

	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(label.Render("Window:") + fmt.Sprintf(" %d spins\n", stats.TotalSpins))
	b.WriteString(label.Render("Red / Black / Green:") + fmt.Sprintf(" %d / %d / %d\n", stats.RedCount, stats.BlackCount, stats.GreenCount))
	b.WriteString(label.Render("Longest streak:") + fmt.Sprintf(" %d red, %d black\n", stats.LongestRed, stats.LongestBlack))
	b.WriteString("\n")
	b.WriteString(label.Render("Hot (most-hit):") + "\n")
	if len(stats.Hot) == 0 {
		b.WriteString("  —\n")
	}
	for _, h := range stats.Hot {
		b.WriteString(fmt.Sprintf("  %s × %d\n", renderHistoryTile(h.Pocket), h.Count))
	}
	b.WriteString("\n")
	b.WriteString(label.Render("Cold (least-hit, still > 0):") + "\n")
	if len(stats.Cold) == 0 {
		b.WriteString("  —\n")
	}
	for _, h := range stats.Cold {
		b.WriteString(fmt.Sprintf("  %s × %d\n", renderHistoryTile(h.Pocket), h.Count))
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorMuted)).
		Italic(true).
		Render("press S / Esc to close"))
	return style.Render(b.String())
}
