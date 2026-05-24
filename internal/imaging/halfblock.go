// Package imaging exposes the half-block ANSI render the chat screen uses to
// inline images posted via URL. Lifted from cmd/ansiconvert's truecolor path
// and pared down — no dither, no quantize, no CLI flags. Each output cell
// covers two source pixels vertically: foreground = top, background = bottom,
// glyph = U+2580 (upper half block).
package imaging

import (
	"image"
	"image/color"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
)

// RenderToANSILines scales `src` to `cols` cells wide (preserving aspect
// ratio, two source pixels per cell row) and emits one ANSI string per cell
// row. Each line starts with the appropriate SGR transition and ends with
// the reset code, so callers can paint them straight into a tea View. Empty
// images or zero cols return nil.
//
// SGR emission deliberately tracks last-fg/last-bg state across cells in a
// row so an unchanged color doesn't re-emit; on long-run images this cuts
// the byte count significantly. Each row starts with the state reset to ""
// so a partially-painted line never leaks into the next.
func RenderToANSILines(src image.Image, cols int) []string {
	if src == nil || cols <= 0 {
		return nil
	}
	scaled := scaleToCells(src, cols)
	cellW := scaled.Rect.Dx()
	cellH := scaled.Rect.Dy() / 2
	if cellW == 0 || cellH == 0 {
		return nil
	}
	out := make([]string, 0, cellH)
	for cy := 0; cy < cellH; cy++ {
		var b strings.Builder
		var lastFg, lastBg string
		for cx := 0; cx < cellW; cx++ {
			top := scaled.NRGBAAt(cx, cy*2)
			bot := scaled.NRGBAAt(cx, cy*2+1)
			fg := truecolorSGR(true, top)
			bg := truecolorSGR(false, bot)
			switch {
			case fg != lastFg && bg != lastBg:
				b.WriteString("\x1b[" + fg + ";" + bg + "m")
			case fg != lastFg:
				b.WriteString("\x1b[" + fg + "m")
			case bg != lastBg:
				b.WriteString("\x1b[" + bg + "m")
			}
			lastFg, lastBg = fg, bg
			b.WriteString("▀")
		}
		b.WriteString("\x1b[0m")
		out = append(out, b.String())
	}
	return out
}

// scaleToCells resizes `src` so its width matches `cellWidth` while keeping
// aspect ratio. Output height is rounded to an even number of pixel rows so
// pairs map cleanly to cells. CatmullRom keeps edges crisp without ringing.
func scaleToCells(src image.Image, cellWidth int) *image.NRGBA {
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	if srcW == 0 || srcH == 0 {
		return image.NewNRGBA(image.Rect(0, 0, cellWidth, 2))
	}
	cellRows := int(float64(srcH) * float64(cellWidth) / float64(srcW) / 2.0)
	if cellRows < 1 {
		cellRows = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, cellWidth, cellRows*2))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func truecolorSGR(fg bool, c color.NRGBA) string {
	base := "38"
	if !fg {
		base = "48"
	}
	return base + ";2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
}
