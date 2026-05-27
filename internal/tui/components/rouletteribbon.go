// Package components — rouletteribbon.go renders the race-track scrolling
// strip of pockets used by the multiplayer roulette screen. The renderer
// is pure: caller drives the `Scroll` offset (frame-driven during spin,
// pinned to the winning pocket once locked) and we lay out the visible
// window of pockets around it.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// RouletteRibbonCells is the number of pocket cells visible in the ribbon
// viewport. Odd so there's a unique center cell where the ball-marker
// lands. 9 cells × 4 cols = 36 cols, fits comfortably in the 60-col cabinet.
const RouletteRibbonCells = 9

// RouletteRibbonCellWidth is the column width of one pocket cell, including
// its left border. Final right border adds one more column to the row.
const RouletteRibbonCellWidth = 4 // "│NN " — separator + 2-char number + space

// RouletteRibbonOpts configures one ribbon paint.
type RouletteRibbonOpts struct {
	// Scroll is the running offset into roulette.RibbonOrder. During spin
	// frames the screen increments this so cells appear to slide past the
	// fixed center marker. Negative or out-of-range values are normalised
	// mod RibbonOrder length.
	Scroll int

	// WinningIdx is the RibbonOrder index of the winning pocket. -1 means
	// "no winner drawn yet" (Betting / NoMoreBets phases) — the ribbon
	// scrolls without highlighting any specific pocket.
	WinningIdx int

	// Locked is true once the spin animation has settled and Scroll has
	// been pinned to centre the winning pocket. When true the marker row
	// gets a brighter colour + the winning number label.
	Locked bool

	// Spinning is true mid-animation (between PhaseSpinning entry and the
	// screen's lockdown tick). Drives the "spinning →" label above the
	// ribbon.
	Spinning bool
}

// RenderRouletteRibbon produces a 5-line block:
//
//	row 0: state label ("spinning ◄◄◄" / "Winner: 17 RED" / blank)
//	row 1: top border
//	row 2: pocket numbers (colored by pocket color)
//	row 3: bottom border
//	row 4: marker arrow + ball glyph
//
// Caller is expected to embed this inside the cabinet body unchanged.
func RenderRouletteRibbon(opts RouletteRibbonOpts) string {
	rib := roulette.RibbonOrder
	mod := len(rib)

	// Center cell lives at (RouletteRibbonCells / 2). The leftmost cell
	// corresponds to Scroll - (cells/2). Mod-wrap so wherever the scroll
	// lands, the ribbon shows a contiguous window.
	half := RouletteRibbonCells / 2
	leftStart := opts.Scroll - half

	// Top/bottom border characters reused for each cell.
	border := "┬"
	leftBorder := "┌"
	rightBorder := "┐"
	bottomLeft := "└"
	bottomMid := "┴"
	bottomRight := "┘"

	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))

	var top, mid, bot strings.Builder
	// Pre-compute styled border rows.
	for i := 0; i < RouletteRibbonCells; i++ {
		if i == 0 {
			top.WriteString(leftBorder)
			bot.WriteString(bottomLeft)
		} else {
			top.WriteString(border)
			bot.WriteString(bottomMid)
		}
		top.WriteString("───")
		bot.WriteString("───")
	}
	top.WriteString(rightBorder)
	bot.WriteString(bottomRight)

	// Middle row: one styled cell per visible pocket.
	for i := 0; i < RouletteRibbonCells; i++ {
		idx := ((leftStart+i)%mod + mod) % mod
		p := rib[idx]
		mid.WriteString(borderStyle.Render("│"))
		mid.WriteString(renderRibbonNumber(p, opts.Locked && idx == opts.WinningIdx))
	}
	mid.WriteString(borderStyle.Render("│"))

	// State label (row 0) and marker row (row 4).
	state := ""
	switch {
	case opts.Spinning:
		state = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorCyan)).
			Italic(true).
			Render("◄◄◄ spinning")
	case opts.Locked && opts.WinningIdx >= 0:
		p := rib[opts.WinningIdx]
		col := pocketColorHex(p)
		state = lipgloss.NewStyle().
			Foreground(lipgloss.Color(col)).
			Bold(true).
			Render("Winner: " + p.Number() + " " + strings.ToUpper(p.Color().String()))
	}

	// Marker arrow + ball positioned at the center cell. Each cell is 4
	// cols wide ("│NN "), so the centre cell's number sits at columns
	// 1..2 of cell index `half`. Total prefix to the centre col 1 is
	// (half * 4) + 1.
	markerCol := half*RouletteRibbonCellWidth + 2
	markerLine := strings.Repeat(" ", markerCol) +
		lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow)).Render("▲") +
		" " +
		lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow)).Bold(true).Render("●")

	return strings.Join([]string{
		state,
		borderStyle.Render(top.String()),
		mid.String(),
		borderStyle.Render(bot.String()),
		markerLine,
	}, "\n")
}

// renderRibbonNumber paints one pocket cell ("NN ") with foreground colour
// matching the pocket and a brighter highlight when this is the winning
// pocket on a locked ribbon.
func renderRibbonNumber(p roulette.Pocket, winner bool) string {
	num := p.Number()
	if len(num) == 1 {
		num = " " + num
	}
	cellText := num + " "
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(pocketColorHex(p)))
	if winner {
		style = style.Bold(true).Background(lipgloss.Color(theme.ColorSurfaceAlt))
	}
	return style.Render(cellText)
}

// pocketColorHex maps a pocket's logical color to a theme hex value.
// Red/black render directly as red/text colours; green (0 and 00) uses the
// existing ColorGreen so the eye reads it as the house pocket.
func pocketColorHex(p roulette.Pocket) string {
	switch p.Color() {
	case roulette.Red:
		return theme.ColorRed
	case roulette.Black:
		return theme.ColorText
	case roulette.Green:
		return theme.ColorGreen
	}
	return theme.ColorText
}
