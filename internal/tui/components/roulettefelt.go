// Package components — roulettefelt.go renders the betting surface for the
// roulette screen: a 0/00 column, a 3×12 number grid (rows top-to-bottom
// hold high, mid, low numbers per column), the dozens row, and the outside-
// bet row. The cursor is a flat int index that this file translates into a
// concrete cell; the screen owns the cursor and forwards arrow keys to
// MoveRouletteCursor for navigation.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Cell indices are arranged in reading order so MoveRouletteCursor can walk
// rows/cols deterministically. Layout:
//
//	row 0 (top row of number grid):     0..11  → 3, 6, 9, ..., 36
//	row 1 (middle row of number grid): 12..23  → 2, 5, 8, ..., 35
//	row 2 (bottom row of number grid): 24..35  → 1, 4, 7, ..., 34
//	row 3 (zeros, only 2 cells):       36..37  → 0, 00
//	row 4 (dozens, 3 cells):           38..40  → 1st12, 2nd12, 3rd12
//	row 5 (outside, 6 cells):          41..46  → low, even, red, black, odd, high
//
// MoveRouletteCursor jumps between rows on up/down and walks within a row
// on left/right; outside the grid we clamp to the row's bounds.
const (
	RouletteCellCount = 47
)

var rouletteCells = func() [RouletteCellCount]roulette.BetKey {
	var out [RouletteCellCount]roulette.BetKey
	// Number grid: column k (0-indexed) holds numbers {3k+1, 3k+2, 3k+3}
	// with the high (3k+3) at the top so the visual matches a real felt.
	for col := 0; col < 12; col++ {
		out[0*12+col] = roulette.BetKey{Type: roulette.BetStraight, Number: roulette.Pocket(3*col + 3)}
		out[1*12+col] = roulette.BetKey{Type: roulette.BetStraight, Number: roulette.Pocket(3*col + 2)}
		out[2*12+col] = roulette.BetKey{Type: roulette.BetStraight, Number: roulette.Pocket(3*col + 1)}
	}
	// Zeros row.
	out[36] = roulette.BetKey{Type: roulette.BetStraight, Number: roulette.Pocket(0)}
	out[37] = roulette.BetKey{Type: roulette.BetStraight, Number: roulette.Pocket00}
	// Dozens row.
	out[38] = roulette.BetKey{Type: roulette.BetDozen1}
	out[39] = roulette.BetKey{Type: roulette.BetDozen2}
	out[40] = roulette.BetKey{Type: roulette.BetDozen3}
	// Outside-bet row.
	out[41] = roulette.BetKey{Type: roulette.BetLow}
	out[42] = roulette.BetKey{Type: roulette.BetEven}
	out[43] = roulette.BetKey{Type: roulette.BetRed}
	out[44] = roulette.BetKey{Type: roulette.BetBlack}
	out[45] = roulette.BetKey{Type: roulette.BetOdd}
	out[46] = roulette.BetKey{Type: roulette.BetHigh}
	return out
}()

// RouletteCellKey returns the BetKey for a given cursor index. Useful for
// the screen to look up "what's the cursor on right now."
func RouletteCellKey(idx int) roulette.BetKey {
	if idx < 0 || idx >= RouletteCellCount {
		return roulette.BetKey{}
	}
	return rouletteCells[idx]
}

// RouletteHotkeyCell maps an outside-bet hotkey (r/b/e/o/L/H/1/2/3) to the
// cursor index of the matching cell. Returns -1 for unmapped keys. Bound
// to the screen's keymap so a hotkey both moves the cursor *and* places a
// chip in one keystroke.
func RouletteHotkeyCell(key string) int {
	switch key {
	case "r", "R":
		return 43
	case "b", "B":
		return 44
	case "e", "E":
		return 42
	case "o", "O":
		return 45
	case "1":
		return 38
	case "2":
		return 39
	case "3":
		return 40
	case "z":
		return 36 // 0
	case "Z":
		return 37 // 00
	}
	return -1
}

// MoveRouletteCursor returns the cursor index after a directional move.
// Direction strings match Bubble Tea's KeyMsg.String() ("up", "down",
// "left", "right"). Movement clamps to row boundaries — at the rightmost
// column of the number grid, "right" stays put rather than wrapping.
func MoveRouletteCursor(cur int, dir string) int {
	if cur < 0 || cur >= RouletteCellCount {
		return 0
	}
	row, col := rouletteRowCol(cur)
	switch dir {
	case "left":
		col--
	case "right":
		col++
	case "up":
		row--
	case "down":
		row++
	}
	return clampRouletteCursor(row, col)
}

// rouletteRowCol decomposes a cursor index into (row, col) for nav math.
func rouletteRowCol(idx int) (row, col int) {
	switch {
	case idx >= 0 && idx <= 35:
		return idx / 12, idx % 12
	case idx == 36, idx == 37:
		return 3, idx - 36
	case idx >= 38 && idx <= 40:
		return 4, idx - 38
	case idx >= 41 && idx <= 46:
		return 5, idx - 41
	}
	return 0, 0
}

// clampRouletteCursor recomposes (row, col) back into an index, clamping
// out-of-bounds positions to the nearest valid cell on the row.
func clampRouletteCursor(row, col int) int {
	if row < 0 {
		row = 0
	}
	if row > 5 {
		row = 5
	}
	switch row {
	case 0, 1, 2:
		if col < 0 {
			col = 0
		}
		if col > 11 {
			col = 11
		}
		return row*12 + col
	case 3:
		if col < 0 {
			col = 0
		}
		if col > 1 {
			col = 1
		}
		return 36 + col
	case 4:
		if col < 0 {
			col = 0
		}
		if col > 2 {
			col = 2
		}
		return 38 + col
	case 5:
		if col < 0 {
			col = 0
		}
		if col > 5 {
			col = 5
		}
		return 41 + col
	}
	return 0
}

// RouletteFeltOpts gathers the per-frame inputs the felt renderer needs.
// MyBets is this user's outstanding stake per cell; Aggregate is the
// table-wide stake (everyone's chips combined). Both are optional — empty
// maps render the felt cleanly.
type RouletteFeltOpts struct {
	Cursor    int
	MyBets    map[roulette.BetKey]int32
	Aggregate map[string]int32
	// Width is the column width the renderer pads its output to. Defaults
	// to 58 (60-col cabinet inner width).
	Width int
}

// RenderRouletteFelt produces a 7-line block: 3 rows of numbers + the zeros
// row + a separator + dozens row + outside-bet row.
func RenderRouletteFelt(opts RouletteFeltOpts) string {
	if opts.Width <= 0 {
		opts.Width = 58
	}
	myBets := opts.MyBets
	agg := opts.Aggregate

	cellWidth := 3 // "NN " (left-padded number + trailing space)
	zerosWidth := 5 // " 0  " or " 00 "
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))

	// Number-grid rows. Each cell is "NN " styled by pocket color, optional
	// cursor highlight, optional aggregate badge.
	var rows [3]strings.Builder
	for row := 0; row < 3; row++ {
		// Left-side: zeros col straddles all three rows visually, but we
		// render it as text on row 1 (middle). On rows 0/2 we pad blank.
		switch row {
		case 0:
			rows[row].WriteString(sep.Render("┌────┬"))
		case 1:
			rows[row].WriteString(renderZerosCell(opts.Cursor == 36, myBets, agg, zerosWidth, roulette.Pocket(0)) + sep.Render("│"))
		case 2:
			rows[row].WriteString(renderZerosCell(opts.Cursor == 37, myBets, agg, zerosWidth, roulette.Pocket00) + sep.Render("│"))
		}
		for col := 0; col < 12; col++ {
			idx := row*12 + col
			rows[row].WriteString(renderFeltNumberCell(idx, opts.Cursor == idx, myBets, agg, cellWidth))
		}
	}

	// Aux row for the zeros column underline. The zeros visually span rows
	// 1+2 of the number grid; row 0 had the top corner; here we close.
	var feltClose strings.Builder
	feltClose.WriteString(sep.Render("└────┴"))
	for col := 0; col < 12; col++ {
		feltClose.WriteString(strings.Repeat("─", cellWidth))
		if col < 11 {
			feltClose.WriteString(sep.Render(""))
		}
	}
	feltClose.WriteString(sep.Render(""))

	// Dozens row — three cells each spanning four number cells.
	dozenWidth := 4 * cellWidth
	var dozens strings.Builder
	dozens.WriteString(strings.Repeat(" ", zerosWidth+1))
	for d := 0; d < 3; d++ {
		idx := 38 + d
		label := []string{"1st 12", "2nd 12", "3rd 12"}[d]
		dozens.WriteString(renderOutsideCell(idx, opts.Cursor == idx, myBets, agg, label, dozenWidth))
	}

	// Outside row — six cells. Each spans two number cells (2 * 3 = 6 cols).
	outWidth := 2 * cellWidth
	var outside strings.Builder
	outside.WriteString(strings.Repeat(" ", zerosWidth+1))
	labels := []string{"1-18", "EVEN", "RED", "BLACK", "ODD", "19-36"}
	for i := 0; i < 6; i++ {
		idx := 41 + i
		outside.WriteString(renderOutsideCell(idx, opts.Cursor == idx, myBets, agg, labels[i], outWidth))
	}

	return strings.Join([]string{
		rows[0].String(),
		rows[1].String(),
		rows[2].String(),
		feltClose.String(),
		dozens.String(),
		outside.String(),
	}, "\n")
}

func renderZerosCell(focused bool, myBets map[roulette.BetKey]int32, agg map[string]int32, width int, p roulette.Pocket) string {
	cellText := " " + p.Number() + " "
	if len(cellText) > width {
		cellText = cellText[:width]
	} else {
		cellText = padCenter(cellText, width)
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorGreen)).Bold(true)
	if focused {
		style = style.Background(lipgloss.Color(theme.ColorSurfaceAlt))
	}
	out := style.Render(cellText)
	key := roulette.BetKey{Type: roulette.BetStraight, Number: p}
	if my := myBets[key]; my > 0 {
		out += badge(my, true)
	} else if a := agg[key.String()]; a > 0 {
		out += badge(a, false)
	}
	return out
}

func renderFeltNumberCell(idx int, focused bool, myBets map[roulette.BetKey]int32, agg map[string]int32, width int) string {
	key := rouletteCells[idx]
	p := key.Number
	num := p.Number()
	if len(num) == 1 {
		num = " " + num
	}
	cellText := num + " "
	if len(cellText) < width {
		cellText += strings.Repeat(" ", width-len(cellText))
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(pocketColorHex(p)))
	if focused {
		style = style.Background(lipgloss.Color(theme.ColorSurfaceAlt)).Bold(true)
	}
	out := style.Render(cellText)
	if my := myBets[key]; my > 0 {
		// Replace the trailing space with a small dot to indicate stake.
		// Keep it inside the cell width so the grid stays aligned.
		out = style.Render(num+"·") +
			lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow)).Render(" ")
	}
	_ = agg
	return out
}

func renderOutsideCell(idx int, focused bool, myBets map[roulette.BetKey]int32, agg map[string]int32, label string, width int) string {
	key := rouletteCells[idx]
	cellText := padCenter(label, width)
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color(outsideForeground(key))).
		Background(lipgloss.Color(outsideBackground(key, focused))).
		Bold(true)
	out := style.Render(cellText)
	if my := myBets[key]; my > 0 {
		out += badge(my, true)
	} else if a := agg[key.String()]; a > 0 {
		out += badge(a, false)
	}
	return out
}

func outsideForeground(k roulette.BetKey) string {
	switch k.Type {
	case roulette.BetRed:
		return theme.ColorRed
	case roulette.BetBlack:
		return theme.ColorText
	default:
		return theme.ColorText
	}
}

func outsideBackground(k roulette.BetKey, focused bool) string {
	if focused {
		return theme.ColorSurfaceAlt
	}
	switch k.Type {
	case roulette.BetRed:
		return theme.ColorSurface
	case roulette.BetBlack:
		return theme.ColorMuted
	}
	return theme.ColorSurface
}

// badge renders a small chip-count indicator. mine=true draws it in yellow
// (the player's own stake); mine=false uses the aggregate-grey tone.
func badge(amount int32, mine bool) string {
	col := theme.ColorAccentDim
	if mine {
		col = theme.ColorYellow
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(col)).
		Bold(mine).
		Render(fmt.Sprintf(" %d", amount))
}

func padCenter(s string, w int) string {
	if len(s) >= w {
		return s[:w]
	}
	left := (w - len(s)) / 2
	right := w - len(s) - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}
