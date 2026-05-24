// Package geocoding turns a place-name query into a short list of
// (lat, lon, canonical-label) candidates. Drives the Profile-screen
// "search by name" affordance on the Locations modal so users don't
// have to type WGS84 decimal degrees by hand.
//
// Backed by Open-Meteo's free geocoding endpoint
// (https://geocoding-api.open-meteo.com/v1/search) — no API key, same
// origin already trusted by the forecast provider, generous quotas for
// the volume a BBS would ever produce.
package geocoding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Result is a single candidate from the geocoder. Latitude / Longitude
// are WGS84 decimal degrees, ready to drop into a saved-location row.
// Country / Admin1 disambiguate the canonical label so the user can tell
// "Paris, Île-de-France, France" apart from "Paris, Texas, US".
type Result struct {
	Name      string
	Country   string
	Admin1    string
	Latitude  float64
	Longitude float64
}

// Canonical renders the disambiguating label the Profile UI shows next
// to each search hit and stores in user_saved_locations.canonical:
//
//	"Paris, Île-de-France, France"
//	"Paris, Texas, United States"
//
// Empty admin/country parts are dropped so smaller hits ("Atlantis")
// don't trail commas.
func (r Result) Canonical() string {
	parts := []string{r.Name}
	if r.Admin1 != "" && r.Admin1 != r.Name {
		parts = append(parts, r.Admin1)
	}
	if r.Country != "" {
		parts = append(parts, r.Country)
	}
	return strings.Join(parts, ", ")
}

// Provider is the geocoder contract. Implementations return up to `max`
// candidates sorted by upstream relevance (typically population × name
// match strength). An empty `query` returns no error and no results.
type Provider interface {
	Search(ctx context.Context, query string, max int) ([]Result, error)
}

const openMeteoBase = "https://geocoding-api.open-meteo.com/v1/search"

// OpenMeteo is the production Provider. NewOpenMeteo's defaults give a
// 5-second timeout — geocoding lookups happen on a user keypress so the
// upper bound matters more than throughput.
type OpenMeteo struct {
	HTTPClient *http.Client
}

func NewOpenMeteo() *OpenMeteo {
	return &OpenMeteo{HTTPClient: &http.Client{Timeout: 5 * time.Second}}
}

// openMeteoResponse mirrors the (loose) JSON shape Open-Meteo returns.
// Fields we don't read decode into nothing since we only declare what we
// consume; the upstream payload carries elevation, population, timezone,
// feature_code and others that don't earn their keep in the UI yet.
type openMeteoResponse struct {
	Results []struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
	} `json:"results"`
}

// Search hits the geocoder and returns the top-`max` candidates. An
// empty / whitespace-only query short-circuits to (nil, nil) so the
// caller can safely call it with raw input.
func (p *OpenMeteo) Search(ctx context.Context, query string, max int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if max <= 0 {
		max = 5
	}
	if max > 10 {
		max = 10
	}
	q := url.Values{}
	q.Set("name", query)
	q.Set("count", strconv.Itoa(max))
	q.Set("language", "en")
	q.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openMeteoBase+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("geocoding: build request: %w", err)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocoding: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocoding: upstream status %d", resp.StatusCode)
	}
	return parseOpenMeteoResults(resp.Body)
}

// parseOpenMeteoResults is split out so the test suite can exercise the
// JSON-decoding path without standing up a live HTTP server.
func parseOpenMeteoResults(body interface {
	Read([]byte) (int, error)
}) ([]Result, error) {
	var raw openMeteoResponse
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("geocoding: decode: %w", err)
	}
	out := make([]Result, 0, len(raw.Results))
	for _, r := range raw.Results {
		out = append(out, Result{
			Name:      r.Name,
			Country:   r.Country,
			Admin1:    r.Admin1,
			Latitude:  r.Latitude,
			Longitude: r.Longitude,
		})
	}
	return out, nil
}

func (p *OpenMeteo) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}
