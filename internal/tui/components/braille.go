package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// BrailleCanvas paints monochrome geometry into terminal cells using the
// Unicode Braille block (U+2800–U+28FF). Each cell covers a 2×4 grid of
// subpixels, mapped to the eight braille dots:
//
//	1 4
//	2 5
//	3 6
//	7 8
//
// Used by the Map screen for road/coastline rendering. Pure compute — no
// SGR, no escape sequences — so the parent screen wraps the output in
// whatever lipgloss style it wants.
type BrailleCanvas struct {
	cols, rows int      // terminal cells
	cells      [][]byte // bit mask per cell (8 dots)
}

// NewBrailleCanvas allocates a `cols × rows` canvas (in TERMINAL CELLS;
// the pixel grid is 2×4 of that).
func NewBrailleCanvas(cols, rows int) *BrailleCanvas {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	cells := make([][]byte, rows)
	for r := range cells {
		cells[r] = make([]byte, cols)
	}
	return &BrailleCanvas{cols: cols, rows: rows, cells: cells}
}

// PixelDims returns the canvas size in subpixels (each terminal cell = 2×4).
func (c *BrailleCanvas) PixelDims() (int, int) { return c.cols * 2, c.rows * 4 }

// Set lights the (x, y) subpixel. OOB calls are silently ignored so callers
// don't have to clip themselves.
func (c *BrailleCanvas) Set(x, y int) {
	if x < 0 || y < 0 {
		return
	}
	cell := x / 2
	cy := y / 4
	if cell >= c.cols || cy >= c.rows {
		return
	}
	dx := x % 2
	dy := y % 4
	c.cells[cy][cell] |= dotBit(dx, dy)
}

// Clear blanks the canvas without reallocating.
func (c *BrailleCanvas) Clear() {
	for r := range c.cells {
		for i := range c.cells[r] {
			c.cells[r][i] = 0
		}
	}
}

// Line draws a straight Bresenham line. Inclusive of both endpoints.
func (c *BrailleCanvas) Line(x0, y0, x1, y1 int) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		c.Set(x0, y0)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// Circle draws a midpoint-algorithm circle of radius r centered at (cx, cy).
func (c *BrailleCanvas) Circle(cx, cy, r int) {
	x, y := r, 0
	err := 1 - x
	for x >= y {
		c.Set(cx+x, cy+y)
		c.Set(cx+y, cy+x)
		c.Set(cx-y, cy+x)
		c.Set(cx-x, cy+y)
		c.Set(cx-x, cy-y)
		c.Set(cx-y, cy-x)
		c.Set(cx+y, cy-x)
		c.Set(cx+x, cy-y)
		y++
		if err < 0 {
			err += 2*y + 1
		} else {
			x--
			err += 2*(y-x) + 1
		}
	}
}

// Render emits one string per row joined by newlines. Empty cells become
// U+2800 (the blank braille pattern, NOT a space) so each row has the same
// visual width — keeps the canvas aligned for column-based math elsewhere.
func (c *BrailleCanvas) Render() string {
	var b strings.Builder
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	for r := 0; r < c.rows; r++ {
		var row strings.Builder
		for col := 0; col < c.cols; col++ {
			row.WriteRune(brailleRune(c.cells[r][col]))
		}
		b.WriteString(style.Render(row.String()))
		if r < c.rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// dotBit maps a (dx ∈ {0,1}, dy ∈ {0,1,2,3}) coordinate to its bit in the
// braille pattern. Standard 8-dot ordering — top-left is bit 0 (dot 1),
// bottom-right is bit 7 (dot 8).
func dotBit(dx, dy int) byte {
	switch dy {
	case 0:
		if dx == 0 {
			return 0x01
		}
		return 0x08
	case 1:
		if dx == 0 {
			return 0x02
		}
		return 0x10
	case 2:
		if dx == 0 {
			return 0x04
		}
		return 0x20
	case 3:
		if dx == 0 {
			return 0x40
		}
		return 0x80
	}
	return 0
}

func brailleRune(mask byte) rune { return rune(0x2800) + rune(mask) }

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
