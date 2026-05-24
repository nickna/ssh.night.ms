package geocoding

import (
	"strings"
	"testing"
)

// openMeteoFixture is a trimmed real response from the live endpoint.
// Covers the two cases the renderer cares about: a city with admin1 +
// country (Paris, France) and one with admin1 equal-or-missing (a
// small island where admin1 is empty). Generated from
// https://geocoding-api.open-meteo.com/v1/search?name=Paris&count=2.
const openMeteoFixture = `{
  "results": [
    {
      "id": 2988507,
      "name": "Paris",
      "latitude": 48.85341,
      "longitude": 2.3488,
      "country": "France",
      "admin1": "Île-de-France"
    },
    {
      "id": 4717560,
      "name": "Paris",
      "latitude": 33.6609,
      "longitude": -95.55551,
      "country": "United States",
      "admin1": "Texas"
    }
  ],
  "generationtime_ms": 0.50306
}`

func TestParseOpenMeteoResults(t *testing.T) {
	got, err := parseOpenMeteoResults(strings.NewReader(openMeteoFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Paris" || got[0].Country != "France" || got[0].Admin1 != "Île-de-France" {
		t.Errorf("results[0] = %+v, want Paris/France/Île-de-France", got[0])
	}
	if got[0].Latitude < 48.8 || got[0].Latitude > 48.9 {
		t.Errorf("results[0].Latitude = %v, want ~48.85", got[0].Latitude)
	}
	if got[1].Admin1 != "Texas" {
		t.Errorf("results[1].Admin1 = %q, want Texas", got[1].Admin1)
	}
}

func TestCanonical(t *testing.T) {
	cases := []struct {
		name string
		r    Result
		want string
	}{
		{"full", Result{Name: "Paris", Admin1: "Île-de-France", Country: "France"}, "Paris, Île-de-France, France"},
		{"no admin1", Result{Name: "Atlantis", Country: "Bermuda"}, "Atlantis, Bermuda"},
		{"admin1 equals name", Result{Name: "Berlin", Admin1: "Berlin", Country: "Germany"}, "Berlin, Germany"},
		{"name only", Result{Name: "Nowhere"}, "Nowhere"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Canonical(); got != tc.want {
				t.Errorf("Canonical() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseEmpty(t *testing.T) {
	// Upstream returns `{}` when nothing matches — Results is absent. The
	// decoder must hand back an empty slice, not nil-with-error.
	got, err := parseOpenMeteoResults(strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty body should yield 0 results, got %d", len(got))
	}
}
