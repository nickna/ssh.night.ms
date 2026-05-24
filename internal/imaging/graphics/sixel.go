package graphics

import "image"

// encodeSixel is a stub. A real implementation would shell out to
// `github.com/mattn/go-sixel` (or hand-roll the encoder) but the dep adds a
// non-trivial transitive footprint and the protocols already covered
// (Kitty, iTerm2, halfblock fallback) reach most users. When this returns
// nil, EncodeWithFallback in graphics.go transparently falls back to the
// halfblock encoder.
func encodeSixel(img image.Image, cellCols int) []string {
	_ = img
	_ = cellCols
	return nil
}
