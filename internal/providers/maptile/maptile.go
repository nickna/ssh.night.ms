// Package maptile pulls a single OpenStreetMap tile and converts it to a
// black-and-white pixel grid suitable for plotting on the BrailleCanvas.
// Intentionally minimal: zoom level 3-6, one tile per fetch, threshold-
// based binarization. The Map screen plots foreground pixels on its
// braille canvas; everything else stays unchanged.
//
// OSM tile usage policy requires a descriptive User-Agent and discourages
// aggressive polling — the TTL cache here caps to one fetch per (z,x,y)
// per process lifetime.
package maptile

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// Tile is the binarized image ready for the BrailleCanvas. Width × Height
// in pixels (not braille cells); plotting code multiplies cell coordinates
// by the 2×4 subpixel factor.
type Tile struct {
	Width, Height int
	// Pixels is row-major. true = foreground (plot), false = background.
	Pixels []bool
	Z, X, Y int
}

// Provider fetches and caches tiles. Cache TTL is "forever" — the OSM
// render rarely changes for a given tile within the process lifetime, and
// the singleflight inside ttlcache coalesces concurrent first-fetches for
// the same (z,x,y) so the map screen panning past a tile boundary issues
// just one HTTP request even with multiple simultaneous viewers.
type Provider struct {
	HTTP      *http.Client
	UserAgent string
	cache     *ttlcache.Cache[tileKey, *Tile]
}

type tileKey struct {
	Z, X, Y int
}

func (k tileKey) String() string { return fmt.Sprintf("%d/%d/%d", k.Z, k.X, k.Y) }

// New returns a Provider with sane defaults. userAgent is required by OSM's
// usage policy; pass an empty string to use the default ssh.night.ms-go UA.
func New(userAgent string) *Provider {
	if userAgent == "" {
		userAgent = "ssh.night.ms-go (https://github.com/nickna/ssh.night.ms)"
	}
	return &Provider{
		HTTP:      &http.Client{Timeout: 8 * time.Second},
		UserAgent: userAgent,
		cache:     ttlcache.New[tileKey, *Tile](0, func(k tileKey) string { return k.String() }),
	}
}

// Tile fetches the (z, x, y) tile from the OSM standard mirror, decodes the
// PNG, and binarizes with a mean-luminance threshold. Cached for the
// process lifetime.
func (p *Provider) Tile(ctx context.Context, z, x, y int) (*Tile, error) {
	key := tileKey{Z: z, X: x, Y: y}
	return p.cache.Get(ctx, key, func(ctx context.Context) (*Tile, error) {
		url := fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", z, x, y)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", p.UserAgent)
		resp, err := p.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("osm: fetch %s: %w", key, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("osm: status %d on %s: %s", resp.StatusCode, key, string(body))
		}
		img, _, err := image.Decode(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("osm: decode %s: %w", key, err)
		}
		tile := binarize(img)
		tile.Z, tile.X, tile.Y = z, x, y
		return tile, nil
	})
}

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

// binarize converts an image to a Tile via a mean-luminance threshold.
// Anything darker than the mean → foreground (plot); lighter → background.
// Matches the visual intuition of OSM: land = light, labels/roads = dark.
func binarize(img image.Image) *Tile {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return &Tile{}
	}
	// Pass 1: mean luminance.
	var total uint64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			total += uint64(lum(img.At(x, y)))
		}
	}
	mean := total / uint64(w*h)
	// Pass 2: threshold.
	pixels := make([]bool, w*h)
	idx := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if uint64(lum(img.At(x, y))) < mean {
				pixels[idx] = true
			}
			idx++
		}
	}
	return &Tile{Width: w, Height: h, Pixels: pixels}
}

// lum is a fast Rec. 601 luma approximation (no gamma). Good enough for the
// "is this pixel light or dark" decision we need.
func lum(c color.Color) uint16 {
	r, g, bl, _ := c.RGBA()
	return uint16((299*r + 587*g + 114*bl) / 1000 >> 8)
}
