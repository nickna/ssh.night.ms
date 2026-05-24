// Package routing turns a (origin, destination, travel-mode) triple into a
// polyline + turn-by-turn step list. Drives the Map screen's directions
// affordance; the same Provider interface lets a unit test swap a fake
// implementation without standing up a real upstream.
//
// One implementation today: OpenRouteService (https://openrouteservice.org),
// chosen for the free-tier API key + multi-modal coverage (drive/walk/cycle)
// + GeoJSON output that side-steps the polyline-decoder we'd otherwise need.
package routing

import (
	"context"
	"errors"
)

// Mode is the travel profile passed straight through to ORS. Defined as a
// string so it serializes into the request URL without conversion.
type Mode string

const (
	ModeDriving Mode = "driving-car"
	ModeWalking Mode = "foot-walking"
	ModeCycling Mode = "cycling-regular"
)

// Label returns a short human form ("drive" / "walk" / "cycle") used by the
// map screen header and toast text.
func (m Mode) Label() string {
	switch m {
	case ModeDriving:
		return "drive"
	case ModeWalking:
		return "walk"
	case ModeCycling:
		return "cycle"
	}
	return string(m)
}

// LatLon mirrors maptile's coordinate shape but lives here so the routing
// package doesn't take a dependency on maptile. WGS84 decimal degrees.
type LatLon struct {
	Lat, Lon float64
}

// Step is one turn-by-turn instruction inside a Route. Distance is in meters
// and Duration is in seconds — both straight from the ORS payload. Name is
// the street/path name when ORS has it, empty otherwise.
type Step struct {
	DistanceMeters  float64
	DurationSeconds float64
	Instruction     string
	Name            string
}

// Route is the result of a successful Route() call. Coordinates is the full
// polyline (sequence of LatLon waypoints; the first matches origin, the
// last matches destination). Distance / Duration are the trip totals.
type Route struct {
	Mode            Mode
	Coordinates     []LatLon
	DistanceMeters  float64
	DurationSeconds float64
	Steps           []Step
}

// Provider is the routing contract. Implementations may cache. A zero-value
// Route is never returned alongside a nil error.
type Provider interface {
	Route(ctx context.Context, origin, dest LatLon, mode Mode) (*Route, error)
}

// ErrRoutingDisabled is returned by providers that need credentials when
// those credentials are absent. The Map screen catches this and surfaces a
// "routing disabled — see operator" toast instead of a generic error.
var ErrRoutingDisabled = errors.New("routing: disabled (no API key configured)")
