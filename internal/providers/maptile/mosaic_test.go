package maptile

import (
	"math"
	"testing"
)

func TestLatLonPixelRoundTrip(t *testing.T) {
	cities := []struct {
		name     string
		lat, lon float64
	}{
		{"Tokyo", 35.6762, 139.6503},
		{"NewYork", 40.7128, -74.0060},
		{"Sydney", -33.8688, 151.2093},
		{"Reykjavik", 64.1466, -21.9426},
		{"Quito", -0.1807, -78.4678},
	}
	for _, c := range cities {
		for z := 2; z <= 18; z++ {
			px, py := LatLonToPixel(c.lat, c.lon, z)
			lat2, lon2 := PixelToLatLon(px, py, z)
			// At z=18 a pixel is ~0.6 m at the equator. Allow 1e-6 deg.
			if math.Abs(lat2-c.lat) > 1e-6 || math.Abs(lon2-c.lon) > 1e-6 {
				t.Errorf("%s z%d: got (%f, %f), want (%f, %f)",
					c.name, z, lat2, lon2, c.lat, c.lon)
			}
		}
	}
}

func TestViewportTilesSingleTile(t *testing.T) {
	// At z=2 the world is a 4×4 grid of tiles. (10°N, 50°E) sits well inside
	// tile (2,2,1) — the global pixel coord is ~(654, 458), far from any
	// tile boundary. A 16×16 viewport stays entirely within that tile.
	tiles, _, _ := ViewportTiles(10, 50, 2, 16, 16)
	if len(tiles) != 1 {
		t.Fatalf("expected 1 tile, got %d (%+v)", len(tiles), tiles)
	}
	if tiles[0].Z != 2 || tiles[0].X != 2 || tiles[0].Y != 1 {
		t.Errorf("expected tile 2/2/1, got %+v", tiles[0])
	}
}

func TestViewportTilesMultiTile(t *testing.T) {
	// A 300×300 viewport at z=2 centered on the equator/prime meridian
	// straddles four tiles: (2,1,1), (2,2,1), (2,1,2), (2,2,2).
	tiles, _, _ := ViewportTiles(0, 0, 2, 300, 300)
	if len(tiles) != 4 {
		t.Fatalf("expected 4 tiles, got %d (%+v)", len(tiles), tiles)
	}
	seen := map[[2]int]bool{}
	for _, tk := range tiles {
		seen[[2]int{tk.X, tk.Y}] = true
	}
	for _, want := range [][2]int{{1, 1}, {2, 1}, {1, 2}, {2, 2}} {
		if !seen[want] {
			t.Errorf("missing tile (%d,%d) in %+v", want[0], want[1], tiles)
		}
	}
}

func TestViewportTilesAntimeridianWrap(t *testing.T) {
	// At z=2, world is 1024 px wide. Center at lon=179.9 puts cx ≈ 1023.7.
	// A 256-px viewport spans roughly 895..1151 — crosses the antimeridian.
	// Tiles wrap so we expect to see X=3 and X=0.
	tiles, _, _ := ViewportTiles(0, 179.9, 2, 256, 8)
	seenX := map[int]bool{}
	for _, tk := range tiles {
		seenX[tk.X] = true
	}
	if !seenX[3] || !seenX[0] {
		t.Errorf("antimeridian wrap missing — got X values %v", seenX)
	}
}

func TestViewportTilesPolarClamp(t *testing.T) {
	// Centering past 85.05° lat would push the slippy y outside [0, 2^z).
	// We don't auto-clamp inputs — callers do — but ViewportTiles itself
	// must skip out-of-range Y rather than emit invalid tile indices.
	// Use a viewport tall enough to step over the top edge.
	tiles, _, _ := ViewportTiles(85.0, 0, 2, 16, 256)
	for _, tk := range tiles {
		if tk.Y < 0 || tk.Y >= 4 {
			t.Errorf("polar clamp failed — got Y=%d at z=2", tk.Y)
		}
	}
}

func TestFloorDivNegative(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{0, 256, 0},
		{255, 256, 0},
		{256, 256, 1},
		{-1, 256, -1},
		{-256, 256, -1},
		{-257, 256, -2},
	}
	for _, c := range cases {
		if got := floorDiv(c.a, c.b); got != c.want {
			t.Errorf("floorDiv(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
