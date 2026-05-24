package imaging

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestRenderToANSILines_SizeAndShape(t *testing.T) {
	// 8x4 source → at cols=4 we get cellRows = 4*4/8/2 = 1, so 1 row of cells
	// representing 2 source rows. Render returns 1 string.
	src := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			src.Set(x, y, color.NRGBA{R: uint8(x * 32), G: uint8(y * 64), B: 0, A: 255})
		}
	}
	lines := RenderToANSILines(src, 4)
	if len(lines) != 1 {
		t.Fatalf("expected 1 cell row, got %d", len(lines))
	}
	// Should contain the upper-half block glyph.
	if !strings.Contains(lines[0], "▀") {
		t.Errorf("missing half-block glyph in %q", lines[0])
	}
	// Should end with the SGR reset.
	if !strings.HasSuffix(lines[0], "\x1b[0m") {
		t.Errorf("missing SGR reset in %q", lines[0])
	}
}

func TestRenderToANSILines_NilInput(t *testing.T) {
	if got := RenderToANSILines(nil, 10); got != nil {
		t.Errorf("nil image should produce nil output, got %v", got)
	}
}

func TestRenderToANSILines_ZeroCols(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	if got := RenderToANSILines(src, 0); got != nil {
		t.Errorf("zero cols should produce nil output, got %v", got)
	}
}
