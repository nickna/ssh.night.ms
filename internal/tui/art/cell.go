// Package art is the ANSI-art pipeline: a CellGrid type backed by a struct
// per terminal cell, an SGR parser that turns a stream of bytes into a
// CellGrid, a file loader for .ans content, and a Bubble Tea-friendly
// renderer. The same pipeline underpins inline image rendering: the
// half-block renderer in internal/imaging feeds CellGrids from PNGs.
package art

import "image/color"

// Cell is one terminal cell: glyph plus styling. RGB colors live in
// color.NRGBA so palette colors and truecolor mix without conversion. A
// zero-value Cell is a transparent-bg space (the renderer treats Bg==nil
// as "skip background").
type Cell struct {
	Rune rune
	Fg   *color.NRGBA // nil = default fg
	Bg   *color.NRGBA // nil = default bg
	Bold bool
	// Italic + Underline omitted for now — common .ans files don't use them.
}

// CellGrid is a fixed-size 2D array of Cells indexed [row][col]. Rendering
// walks rows top-to-bottom.
type CellGrid struct {
	Width, Height int
	Cells         [][]Cell
}

// NewCellGrid allocates a Width×Height grid pre-filled with empty cells.
func NewCellGrid(width, height int) *CellGrid {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	cells := make([][]Cell, height)
	for i := range cells {
		cells[i] = make([]Cell, width)
		for j := range cells[i] {
			cells[i][j] = Cell{Rune: ' '}
		}
	}
	return &CellGrid{Width: width, Height: height, Cells: cells}
}

// At returns a pointer to the cell at (row, col) — useful for the parser
// when it walks the canvas and writes glyphs in-place. Returns nil for OOB
// coordinates so the parser can safely ignore content past the canvas.
func (g *CellGrid) At(row, col int) *Cell {
	if row < 0 || row >= g.Height || col < 0 || col >= g.Width {
		return nil
	}
	return &g.Cells[row][col]
}

// Rgb is a convenience constructor for an opaque RGB color pointer.
func Rgb(r, g, b uint8) *color.NRGBA {
	return &color.NRGBA{R: r, G: g, B: b, A: 0xFF}
}
