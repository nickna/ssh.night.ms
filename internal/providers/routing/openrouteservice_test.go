package routing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const sampleORSPayload = `{
  "features": [{
    "geometry": {
      "coordinates": [[2.0, 48.0], [2.1, 48.1], [2.2, 48.2]]
    },
    "properties": {
      "summary": {"distance": 15000, "duration": 900},
      "segments": [{
        "steps": [
          {"distance": 120, "duration": 15, "instruction": "Head north", "name": "Main St"},
          {"distance": 240, "duration": 30, "instruction": "Turn left", "name": "5th Ave"}
        ]
      }]
    }
  }]
}`

func TestRouteErrRoutingDisabled(t *testing.T) {
	p := NewOpenRouteService("")
	_, err := p.Route(context.Background(), LatLon{0, 0}, LatLon{1, 1}, ModeDriving)
	if !errors.Is(err, ErrRoutingDisabled) {
		t.Fatalf("expected ErrRoutingDisabled, got %v", err)
	}
}

func TestRouteParsesGeoJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v2/directions/driving-car/geojson") {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "test-key" {
			t.Errorf("missing API key header — got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `[2,48]`) {
			t.Errorf("body missing origin in [lon,lat] order: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleORSPayload))
	}))
	defer server.Close()

	p := NewOpenRouteService("test-key")
	p.BaseURL = server.URL
	route, err := p.Route(context.Background(),
		LatLon{Lat: 48, Lon: 2}, LatLon{Lat: 48.2, Lon: 2.2}, ModeDriving)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Mode != ModeDriving {
		t.Errorf("mode: got %q, want driving-car", route.Mode)
	}
	if route.DistanceMeters != 15000 {
		t.Errorf("distance: got %f, want 15000", route.DistanceMeters)
	}
	if route.DurationSeconds != 900 {
		t.Errorf("duration: got %f, want 900", route.DurationSeconds)
	}
	if len(route.Coordinates) != 3 {
		t.Fatalf("coords: got %d, want 3", len(route.Coordinates))
	}
	// Verify [lon,lat] → LatLon{Lat, Lon} flip.
	if route.Coordinates[0].Lat != 48 || route.Coordinates[0].Lon != 2 {
		t.Errorf("first coord wrong: %+v", route.Coordinates[0])
	}
	if len(route.Steps) != 2 {
		t.Fatalf("steps: got %d, want 2", len(route.Steps))
	}
	if route.Steps[0].Name != "Main St" {
		t.Errorf("step 0 name: got %q, want Main St", route.Steps[0].Name)
	}
	if route.Steps[1].Instruction != "Turn left" {
		t.Errorf("step 1 instr: got %q, want Turn left", route.Steps[1].Instruction)
	}
}

func TestRouteCacheCoalesces(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleORSPayload))
	}))
	defer server.Close()

	p := NewOpenRouteService("test-key")
	p.BaseURL = server.URL
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := p.Route(ctx, LatLon{48, 2}, LatLon{48.2, 2.2}, ModeDriving)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 upstream call (cache hits), got %d", got)
	}
}

func TestRouteModeSplitsCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleORSPayload))
	}))
	defer server.Close()

	p := NewOpenRouteService("test-key")
	p.BaseURL = server.URL
	ctx := context.Background()
	for _, mode := range []Mode{ModeDriving, ModeWalking, ModeCycling} {
		_, err := p.Route(ctx, LatLon{48, 2}, LatLon{48.2, 2.2}, mode)
		if err != nil {
			t.Fatalf("Route(%s): %v", mode, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 upstream calls (one per mode), got %d", got)
	}
}

func TestRouteNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream choked"))
	}))
	defer server.Close()

	p := NewOpenRouteService("test-key")
	p.BaseURL = server.URL
	_, err := p.Route(context.Background(), LatLon{0, 0}, LatLon{1, 1}, ModeDriving)
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error doesn't mention status: %v", err)
	}
}

func TestModeLabel(t *testing.T) {
	cases := []struct {
		in   Mode
		want string
	}{
		{ModeDriving, "drive"},
		{ModeWalking, "walk"},
		{ModeCycling, "cycle"},
	}
	for _, c := range cases {
		if got := c.in.Label(); got != c.want {
			t.Errorf("Label(%s): got %q, want %q", c.in, got, c.want)
		}
	}
}
