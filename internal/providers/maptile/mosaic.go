package maptile

import "math"

// LatLonToTile converts a (lat, lon) WGS84 coordinate to the slippy-map
// (x, y) tile indexes at the given zoom. Returns float so callers can
// derive the within-tile fractional position too.
func LatLonToTile(lat, lon float64, z int) (xf, yf float64) {
	n := math.Pow(2, float64(z))
	xf = (lon + 180) / 360 * n
	latRad := lat * math.Pi / 180
	yf = (1 - math.Log(math.Tan(latRad)+1/math.Cos(latRad))/math.Pi) / 2 * n
	return
}

// LatLonToPixel maps WGS84 to global pixel coords at the given zoom level
// (one tile = 256 px). Used to position both the viewport origin and any
// overlay geometry (route polylines, markers) in mosaic pixel space.
func LatLonToPixel(lat, lon float64, z int) (px, py float64) {
	xf, yf := LatLonToTile(lat, lon, z)
	return xf * 256, yf * 256
}

// PixelToLatLon is the inverse of LatLonToPixel. Used when panning: shift
// the center by a pixel delta, then convert back to lat/lon for the next
// tile fetch round.
func PixelToLatLon(px, py float64, z int) (lat, lon float64) {
	n := math.Pow(2, float64(z))
	lon = px/256/n*360 - 180
	latRad := math.Atan(math.Sinh(math.Pi * (1 - 2*py/256/n)))
	lat = latRad * 180 / math.Pi
	return
}

// TileKey identifies a tile to fetch plus where its top-left pixel lands
// inside the composed mosaic. DrawX/DrawY may be negative or extend past
// the mosaic bounds — image/draw clips automatically.
type TileKey struct {
	Z, X, Y      int
	DrawX, DrawY int
}

// ViewportTiles enumerates the (z, x, y) tiles needed to fill an
// (imgW × imgH)-pixel viewport centered on (centerLat, centerLon) at zoom
// z. Longitude wraps at the antimeridian; latitude clamps at the poles
// (the slippy grid has no tiles above y=0 or below y=2^z - 1).
//
// originX / originY are the global-pixel coords of the mosaic's top-left
// corner. Callers subtract those from any overlay's global pixel coords to
// place it correctly inside the mosaic.
func ViewportTiles(centerLat, centerLon float64, z, imgW, imgH int) (tiles []TileKey, originX, originY int) {
	cx, cy := LatLonToPixel(centerLat, centerLon, z)
	ox := int(math.Floor(cx - float64(imgW)/2))
	oy := int(math.Floor(cy - float64(imgH)/2))
	tx0 := floorDiv(ox, 256)
	ty0 := floorDiv(oy, 256)
	tx1 := floorDiv(ox+imgW-1, 256)
	ty1 := floorDiv(oy+imgH-1, 256)
	nMax := 1 << uint(z) // 2^z
	for ty := ty0; ty <= ty1; ty++ {
		if ty < 0 || ty >= nMax {
			continue
		}
		for tx := tx0; tx <= tx1; tx++ {
			wrapX := ((tx % nMax) + nMax) % nMax
			tiles = append(tiles, TileKey{
				Z:     z,
				X:     wrapX,
				Y:     ty,
				DrawX: tx*256 - ox,
				DrawY: ty*256 - oy,
			})
		}
	}
	return tiles, ox, oy
}

// floorDiv is integer division that floors toward negative infinity. Go's
// `/` truncates toward zero, which would put tx0=0 for ox=-1 — wrong.
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
