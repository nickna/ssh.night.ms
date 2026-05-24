package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
)

// GenerateIdenticon returns a PNG of a deterministic geometric avatar for
// `handle`. Mirrors the .NET IdenticonRenderer shape: 5x5 cells mirrored to
// 10 cols wide on a square canvas, with the foreground color seeded from the
// hash so two handles never share an identicon by accident.
//
// `size` is the total canvas pixel size; each cell becomes size/10 px. Sub-10
// sizes round to a 10px minimum so cells still render as single pixels.
func GenerateIdenticon(handle string, size int) ([]byte, error) {
	if size < 10 {
		size = 10
	}
	// Snap to a multiple of 10 so the cells tile cleanly. This avoids
	// off-by-one stripes on the right/bottom edges of the canvas.
	cell := size / 10
	size = cell * 10

	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(handle))))

	// Foreground color: pick from a palette derived from the night.ms TUI
	// theme so identicons feel of-a-piece with the rest of the UI. The
	// first byte picks the slot; the rest of the bytes drive the grid.
	fg := identiconPalette[int(digest[0])%len(identiconPalette)]
	bg := color.RGBA{R: 0x24, G: 0x1D, B: 0x33, A: 0xFF} // surface-alt

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// Fill background.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, bg)
		}
	}

	// 5 columns × 10 rows of cells, mirrored across the vertical axis. That
	// gives us 5×10 = 50 cells; we have 32 bytes of hash to draw from, so
	// each cell consumes one bit out of a rolling stream starting at byte 1.
	// 50 bits ≤ 32 bytes × 8, plenty of headroom.
	bits := func(idx int) bool {
		b := digest[1+idx/8]
		return (b>>(idx%8))&1 == 1
	}
	for row := 0; row < 10; row++ {
		for col := 0; col < 5; col++ {
			if !bits(row*5 + col) {
				continue
			}
			fillCell(img, col, row, cell, fg)
			fillCell(img, 9-col, row, cell, fg) // mirror
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("identicon: encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// fillCell paints one identicon cell. We avoid image/draw to keep the
// function dependency-light — direct Set calls suffice at identicon sizes.
func fillCell(img *image.RGBA, col, row, cell int, c color.Color) {
	x0 := col * cell
	y0 := row * cell
	for y := y0; y < y0+cell; y++ {
		for x := x0; x < x0+cell; x++ {
			img.Set(x, y, c)
		}
	}
}

// identiconPalette samples from the lipgloss palette used by the TUI so the
// in-browser avatars match the in-terminal look. Eight slots give enough
// distinct buckets that handle collisions on the first byte rarely produce
// the same color.
var identiconPalette = []color.RGBA{
	{R: 0xFF, G: 0x7D, B: 0xB0, A: 0xFF}, // accent pink
	{R: 0xFF, G: 0xD1, B: 0x66, A: 0xFF}, // yellow
	{R: 0x5E, G: 0xE3, B: 0x9C, A: 0xFF}, // green
	{R: 0x5E, G: 0xE7, B: 0xDF, A: 0xFF}, // cyan
	{R: 0xFF, G: 0x6B, B: 0x7A, A: 0xFF}, // red
	{R: 0x9C, G: 0x8A, B: 0xA5, A: 0xFF}, // accent-dim
	{R: 0xC9, G: 0xA0, B: 0xDC, A: 0xFF}, // lavender (custom)
	{R: 0xA0, G: 0xE6, B: 0xC7, A: 0xFF}, // mint (custom)
}
