// Package maptile pulls OpenStreetMap raster tiles and hands them back as
// decoded color images, ready to compose into a half-block mosaic. Tiles are
// cached for the process lifetime; the singleflight inside ttlcache coalesces
// concurrent first-fetches for the same (z, x, y) so a pan storm issues just
// one HTTP request per tile.
//
// OSM tile usage policy requires a descriptive User-Agent and discourages
// aggressive polling — the cache + singleflight here keep us inside the
// "fair use" envelope for hobby traffic.
package maptile

import (
	"context"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// Tile is the decoded OSM raster, kept in full color so the caller can
// composite it into an RGBA mosaic before half-block conversion.
type Tile struct {
	Z, X, Y int
	Image   image.Image
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
// PNG, and returns the color image. Cached for the process lifetime.
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
		return &Tile{Z: z, X: x, Y: y, Image: img}, nil
	})
}
