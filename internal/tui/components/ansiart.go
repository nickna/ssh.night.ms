package components

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/art"
)

// RenderCellGrid walks a CellGrid and returns a string with SGR escapes for
// each run of same-style cells. Runs are batched per row so the output
// doesn't churn the renderer state once per cell — measurably faster on
// larger pieces.
func RenderCellGrid(g *art.CellGrid) string {
	if g == nil {
		return ""
	}
	var b strings.Builder
	for r := 0; r < g.Height; r++ {
		row := g.Cells[r]
		i := 0
		for i < len(row) {
			j := i + 1
			for j < len(row) && sameStyle(row[i], row[j]) {
				j++
			}
			b.WriteString(styleFor(row[i]).Render(runesFromRange(row[i:j])))
			i = j
		}
		if r < g.Height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func sameStyle(a, b art.Cell) bool {
	return colorsEq(a.Fg, b.Fg) && colorsEq(a.Bg, b.Bg) && a.Bold == b.Bold
}

func colorsEq(a, b *color.NRGBA) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func styleFor(c art.Cell) lipgloss.Style {
	s := lipgloss.NewStyle()
	if c.Fg != nil {
		s = s.Foreground(lipgloss.Color(hexFromColor(*c.Fg)))
	}
	if c.Bg != nil {
		s = s.Background(lipgloss.Color(hexFromColor(*c.Bg)))
	}
	if c.Bold {
		s = s.Bold(true)
	}
	return s
}

func hexFromColor(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

func runesFromRange(cells []art.Cell) string {
	var b strings.Builder
	b.Grow(len(cells))
	for _, c := range cells {
		if c.Rune == 0 {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(c.Rune)
	}
	return b.String()
}
