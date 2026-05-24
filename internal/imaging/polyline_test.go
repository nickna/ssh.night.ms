package imaging

import (
	"image"
	"image/color"
	"testing"
)

func TestDrawLineHorizontal(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 10, 10))
	red := color.RGBA{255, 0, 0, 255}
	DrawLine(dst, 2, 5, 7, 5, red)
	for x := 2; x <= 7; x++ {
		if got := dst.RGBAAt(x, 5); got != red {
			t.Errorf("pixel (%d, 5) = %+v, want red", x, got)
		}
	}
	// Out-of-line pixel should be untouched (alpha 0).
	if dst.RGBAAt(0, 0).A != 0 {
		t.Errorf("expected (0,0) untouched, got %+v", dst.RGBAAt(0, 0))
	}
}

func TestDrawLineDiagonal(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 5, 5))
	c := color.RGBA{0, 255, 0, 255}
	DrawLine(dst, 0, 0, 4, 4, c)
	for i := 0; i <= 4; i++ {
		if dst.RGBAAt(i, i) != c {
			t.Errorf("diagonal pixel (%d, %d) not lit", i, i)
		}
	}
}

func TestDrawLineClipping(t *testing.T) {
	// Line extends past the canvas — must not panic, and visible portion
	// must still draw correctly.
	dst := image.NewRGBA(image.Rect(0, 0, 5, 5))
	c := color.RGBA{0, 0, 255, 255}
	DrawLine(dst, -10, 2, 10, 2, c)
	for x := 0; x <= 4; x++ {
		if dst.RGBAAt(x, 2) != c {
			t.Errorf("clipped pixel (%d, 2) not lit", x)
		}
	}
}

func TestDrawCrosshair(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 11, 11))
	c := color.RGBA{255, 255, 0, 255}
	DrawCrosshair(dst, 5, 5, 3, c)
	// Horizontal arm.
	for x := 2; x <= 8; x++ {
		if dst.RGBAAt(x, 5) != c {
			t.Errorf("horizontal arm pixel (%d, 5) not lit", x)
		}
	}
	// Vertical arm.
	for y := 2; y <= 8; y++ {
		if dst.RGBAAt(5, y) != c {
			t.Errorf("vertical arm pixel (5, %d) not lit", y)
		}
	}
}

func TestDrawPolyline(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 20, 20))
	c := color.RGBA{255, 0, 255, 255}
	// L-shape: (2,2) → (2,10) → (15,10).
	pts := []image.Point{{2, 2}, {2, 10}, {15, 10}}
	DrawPolyline(dst, pts, 0, c)
	// Vertical leg.
	for y := 2; y <= 10; y++ {
		if dst.RGBAAt(2, y) != c {
			t.Errorf("vertical leg pixel (2, %d) not lit", y)
		}
	}
	// Horizontal leg.
	for x := 2; x <= 15; x++ {
		if dst.RGBAAt(x, 10) != c {
			t.Errorf("horizontal leg pixel (%d, 10) not lit", x)
		}
	}
}

func TestDrawPolylineThick(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 20, 20))
	c := color.RGBA{0, 255, 255, 255}
	pts := []image.Point{{5, 10}, {15, 10}}
	DrawPolyline(dst, pts, 1, c)
	// With radius=1 the line is 3px tall (rows 9, 10, 11) for every x in [5..15].
	for _, y := range []int{9, 10, 11} {
		for x := 5; x <= 15; x++ {
			if dst.RGBAAt(x, y) != c {
				t.Errorf("thick line missing pixel (%d, %d)", x, y)
			}
		}
	}
}

func TestDrawMarker(t *testing.T) {
	dst := image.NewRGBA(image.Rect(0, 0, 11, 11))
	c := color.RGBA{255, 100, 100, 255}
	DrawMarker(dst, 5, 5, 2, c)
	// Center should be lit.
	if dst.RGBAAt(5, 5) != c {
		t.Errorf("marker center not lit")
	}
	// Adjacent pixels within radius 2 should be lit (e.g., (3,5) is dist 2).
	for _, p := range []image.Point{{3, 5}, {7, 5}, {5, 3}, {5, 7}, {4, 4}} {
		if dst.RGBAAt(p.X, p.Y) != c {
			t.Errorf("marker pixel (%d, %d) not lit", p.X, p.Y)
		}
	}
	// A pixel outside radius 2 should NOT be lit — e.g., (5,8) is dist 3.
	if dst.RGBAAt(5, 8).A != 0 {
		t.Errorf("marker pixel (5, 8) lit but should be outside radius")
	}
}

func TestDrawMarkerOffCanvas(t *testing.T) {
	// Marker partially off-canvas — must not panic and visible portion must lit.
	dst := image.NewRGBA(image.Rect(0, 0, 5, 5))
	c := color.RGBA{0, 0, 255, 255}
	DrawMarker(dst, 0, 0, 3, c)
	if dst.RGBAAt(0, 0) != c {
		t.Errorf("clipped marker center not lit")
	}
}
