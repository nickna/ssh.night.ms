package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
)

// OpenRouteService talks to https://openrouteservice.org/'s Directions v2
// endpoint. The free tier issues an API key per signup and meters ~2000
// requests/day / 40/minute. A 5-minute TTL cache here de-duplicates panning
// noise — the same (mode, origin, dest) lookup re-uses the prior payload.
type OpenRouteService struct {
	HTTPClient *http.Client
	APIKey     string

	// BaseURL is overridden by tests pointing at httptest.NewServer; defaults
	// to the public ORS endpoint.
	BaseURL string

	cache *ttlcache.Cache[routeKey, *Route]
}

// routeKey is the cache key. Coordinates are rounded to 4 decimal places
// (~11 m at the equator) so micro-jitter in origin/dest doesn't bust the
// cache; mode is the ORS profile string.
type routeKey struct {
	Mode                                   string
	OriginLat, OriginLon, DestLat, DestLon int64
}

const orsBaseURL = "https://api.openrouteservice.org"

// NewOpenRouteService builds a provider with a 10 s HTTP timeout and a 5
// min response cache. Pass an empty apiKey to force the provider into the
// disabled state — Route then returns ErrRoutingDisabled without hitting
// the network.
func NewOpenRouteService(apiKey string) *OpenRouteService {
	return &OpenRouteService{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		APIKey:     apiKey,
		BaseURL:    orsBaseURL,
		cache: ttlcache.New[routeKey, *Route](5*time.Minute, func(k routeKey) string {
			return fmt.Sprintf("%s:%d:%d:%d:%d", k.Mode,
				k.OriginLat, k.OriginLon, k.DestLat, k.DestLon)
		}),
	}
}

// Route fires a Directions request and returns the decoded Route. An empty
// API key collapses to ErrRoutingDisabled immediately; transient upstream
// failures propagate so the screen can show the message.
func (p *OpenRouteService) Route(ctx context.Context, origin, dest LatLon, mode Mode) (*Route, error) {
	if p.APIKey == "" {
		return nil, ErrRoutingDisabled
	}
	key := routeKey{
		Mode:      string(mode),
		OriginLat: int64(origin.Lat * 10000),
		OriginLon: int64(origin.Lon * 10000),
		DestLat:   int64(dest.Lat * 10000),
		DestLon:   int64(dest.Lon * 10000),
	}
	return p.cache.Get(ctx, key, func(ctx context.Context) (*Route, error) {
		return p.fetchRoute(ctx, origin, dest, mode)
	})
}

func (p *OpenRouteService) fetchRoute(ctx context.Context, origin, dest LatLon, mode Mode) (*Route, error) {
	body := map[string]any{
		// ORS expects [lon, lat] pairs.
		"coordinates":  [][]float64{{origin.Lon, origin.Lat}, {dest.Lon, dest.Lat}},
		"instructions": true,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("routing: marshal: %w", err)
	}
	endpoint := p.BaseURL + "/v2/directions/" + string(mode) + "/geojson"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("routing: build request: %w", err)
	}
	req.Header.Set("Authorization", p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("routing: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("routing: status %d: %s", resp.StatusCode, string(errBody))
	}
	return parseORSResponse(resp.Body, mode)
}

func (p *OpenRouteService) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

// orsResponse mirrors the GeoJSON FeatureCollection ORS returns. We pull
// just enough fields to draw a polyline + populate the steps overlay; ORS
// also returns bbox, way_points, extras etc. that we ignore.
type orsResponse struct {
	Features []struct {
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"` // [lon, lat] pairs
		} `json:"geometry"`
		Properties struct {
			Summary struct {
				Distance float64 `json:"distance"`
				Duration float64 `json:"duration"`
			} `json:"summary"`
			Segments []struct {
				Steps []struct {
					Distance    float64 `json:"distance"`
					Duration    float64 `json:"duration"`
					Instruction string  `json:"instruction"`
					Name        string  `json:"name"`
				} `json:"steps"`
			} `json:"segments"`
		} `json:"properties"`
	} `json:"features"`
}

// parseORSResponse decodes the ORS GeoJSON FeatureCollection into our Route
// struct. Split out so the test suite can exercise the parser directly.
// Coordinate pairs are flipped from ORS's [lon, lat] convention into our
// LatLon{Lat, Lon} layout.
func parseORSResponse(r io.Reader, mode Mode) (*Route, error) {
	var raw orsResponse
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("routing: decode: %w", err)
	}
	if len(raw.Features) == 0 {
		return nil, errors.New("routing: empty FeatureCollection")
	}
	f := raw.Features[0]
	out := &Route{
		Mode:            mode,
		DistanceMeters:  f.Properties.Summary.Distance,
		DurationSeconds: f.Properties.Summary.Duration,
		Coordinates:     make([]LatLon, 0, len(f.Geometry.Coordinates)),
	}
	for _, c := range f.Geometry.Coordinates {
		if len(c) >= 2 {
			out.Coordinates = append(out.Coordinates, LatLon{Lat: c[1], Lon: c[0]})
		}
	}
	for _, seg := range f.Properties.Segments {
		for _, s := range seg.Steps {
			out.Steps = append(out.Steps, Step{
				DistanceMeters:  s.Distance,
				DurationSeconds: s.Duration,
				Instruction:     s.Instruction,
				Name:            s.Name,
			})
		}
	}
	return out, nil
}
