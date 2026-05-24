package imaging

import (
	"image"
	"image/color"
)

// DrawLine plots a Bresenham line from (x0,y0) to (x1,y1) onto dst. The
// stroke is 1 pixel thick; for thicker lines call repeatedly with a small
// perpendicular offset or use the polyline helper. Pixels outside dst's
// bounds are silently skipped, so off-canvas geometry clips for free.
func DrawLine(dst *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx + dy
	x, y := x0, y0
	for {
		dst.Set(x, y, c)
		if x == x1 && y == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			if x == x1 {
				return
			}
			err += dy
			x += sx
		}
		if e2 <= dx {
			if y == y1 {
				return
			}
			err += dx
			y += sy
		}
	}
}

// DrawCrosshair stamps a small "+" centered on (x, y) with the given half-
// length. Used by the map screen to mark the viewport center on top of the
// composed tile mosaic.
func DrawCrosshair(dst *image.RGBA, x, y, halfLen int, c color.Color) {
	DrawLine(dst, x-halfLen, y, x+halfLen, y, c)
	DrawLine(dst, x, y-halfLen, x, y+halfLen, c)
}

// DrawPolyline draws a multi-segment line connecting pts in order. radius
// thickens each segment by stamping a disk of the given radius at every
// pixel along the Bresenham trace; radius=0 falls back to a single-pixel
// line. Used by the map screen to paint a route on top of the mosaic.
func DrawPolyline(dst *image.RGBA, pts []image.Point, radius int, c color.Color) {
	if len(pts) < 2 {
		return
	}
	if radius <= 0 {
		for i := 1; i < len(pts); i++ {
			DrawLine(dst, pts[i-1].X, pts[i-1].Y, pts[i].X, pts[i].Y, c)
		}
		return
	}
	for i := 1; i < len(pts); i++ {
		drawThickLine(dst, pts[i-1].X, pts[i-1].Y, pts[i].X, pts[i].Y, radius, c)
	}
}

// DrawMarker stamps a filled disk of the given radius centered on (x, y).
// Used by the map screen for origin (green) / destination (red) endpoints.
// Bounds-checks every pixel so off-canvas markers clip cleanly.
func DrawMarker(dst *image.RGBA, x, y, radius int, fill color.Color) {
	if radius <= 0 {
		dst.Set(x, y, fill)
		return
	}
	r2 := radius * radius
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= r2 {
				dst.Set(x+dx, y+dy, fill)
			}
		}
	}
}

// drawThickLine is Bresenham with a disk stamp at each step — gives a
// rough antialias-free thick line that's still cheap to render. Bounds
// checks are deferred to dst.Set so callers can pass off-canvas endpoints.
func drawThickLine(dst *image.RGBA, x0, y0, x1, y1, radius int, c color.Color) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx + dy
	x, y := x0, y0
	r2 := radius * radius
	for {
		for dyOff := -radius; dyOff <= radius; dyOff++ {
			for dxOff := -radius; dxOff <= radius; dxOff++ {
				if dxOff*dxOff+dyOff*dyOff <= r2 {
					dst.Set(x+dxOff, y+dyOff, c)
				}
			}
		}
		if x == x1 && y == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			if x == x1 {
				return
			}
			err += dy
			x += sx
		}
		if e2 <= dx {
			if y == y1 {
				return
			}
			err += dx
			y += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
