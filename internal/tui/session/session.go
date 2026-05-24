// Package session holds the per-SSH-session context passed down to every
// screen. Lives in its own package so screens, the root model, and view
// components can all reference it without a dep cycle.
package session

import (
	"context"
	"time"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
)

// artGalleryEntry re-exports art.GalleryEntry via a type alias so the
// session.GalleryEntry name resolves locally for the screens package.
type artGalleryEntry = art.GalleryEntry

// WeatherDefaults holds the fallback lat/lon used when the user hasn't
// saved a location. Sourced from NIGHTMS_WEATHER_LAT / LON / LABEL.
type WeatherDefaults struct {
	Lat   float64
	Lon   float64
	Label string
}

// GalleryProvider is the contract the Gallery screen depends on. Implemented
// by art.FileSystemGallery; defined here so screens don't import the art
// package's loader directly.
type GalleryProvider interface {
	List() ([]GalleryEntry, error)
}

// GalleryEntry mirrors art.GalleryEntry so screens can stay decoupled from
// the loader implementation.
type GalleryEntry = artGalleryEntry

// State is the mutable per-user slice of a Session: who is logged in, what
// they last saved, what their terminal looks like right now. Everything in
// here is written-to during the session lifetime; everything outside (in the
// embedded *Deps groups) is read-only after construction.
type State struct {
	Identity auth.Known

	// Width / Height update on every tea.WindowSizeMsg.
	Width  int
	Height int

	// PrimaryLocation is the user's preferred saved location (lowest
	// sort_order in user_saved_locations). nil means "use the env-var
	// defaults via WeatherDefaults". Loaded once at login by the transport
	// layer; refreshed by the Profile screen after mutating the list.
	PrimaryLocation *realtime.SavedLocation

	// DisplayPrefs is the cached time / date / clock format slice of the
	// user row. Loaded once at session-attach so render hot paths (status
	// bar clock, chat timestamps, /finger) don't pay a per-render query.
	// Refreshed via RefreshDisplayPrefs after the Profile screen saves.
	// Zero value is UTC + ISO + 24-hour — matches a fresh row's defaults.
	DisplayPrefs DisplayPrefs
}

// Session is the read-mostly per-session bag of dependencies, plus the
// mutable State slice. Sub-structs are embedded so screens keep accessing
// fields flat: sess.Pool, sess.Chat, sess.Weather all resolve via field
// promotion without callers caring about the grouping.
type Session struct {
	// SSHContext is the underlying ssh.Context (Go context.Context-compatible)
	// — done channel fires when the SSH connection drops. Goroutines started
	// from screens (presence heartbeat, post-signup hooks) bind to it so they
	// exit cleanly on disconnect. For web sessions this is the request ctx.
	SSHContext context.Context

	// Graphics is the detected terminal-image protocol (Kitty/iTerm2/Sixel/
	// Halfblock) the inline-image renderer should dispatch through. Computed
	// per-session from $TERM + SSH-forwarded environ (transport) or fixed at
	// Halfblock (web — xterm.js renders the half-block fallback).
	Graphics graphics.Protocol

	// WeatherDefaults flows through to every Session via WeatherCoords().
	// Copied off PolicyDeps at session-attach to keep the WeatherCoords()
	// receiver free of indirection.
	WeatherDefaults WeatherDefaults

	// Embedded process-singleton groups: field promotion keeps screen access
	// flat (sess.Pool, sess.Chat, sess.News, ...) even though the underlying
	// structure is grouped.
	CoreDeps
	RealtimeDeps
	ProviderDeps
	ArtDeps
	GameDeps

	// Embedded mutable state: same flat-access convenience (sess.Identity,
	// sess.Width, sess.DisplayPrefs, ...).
	State
}

// New builds a Session from the process-singleton Deps plus the per-session
// inputs both entry points (SSH and web) need to supply: who logged in, what
// their terminal dimensions are, what graphics protocol they negotiated, and
// the session-scoped context whose Done channel terminates per-session
// goroutines on disconnect.
//
// Identity / PrimaryLocation / DisplayPrefs are passed in as a *State so the
// caller can pre-warm them with per-user DB reads (the transport layer does
// this for both Locations and the users row) without New having to know about
// the queries package.
func New(deps Deps, st State, sshCtx context.Context, gfx graphics.Protocol) *Session {
	return &Session{
		SSHContext:      sshCtx,
		Graphics:        gfx,
		WeatherDefaults: deps.Policy.WeatherDefaults,
		CoreDeps:        deps.Core,
		RealtimeDeps:    deps.Realtime,
		ProviderDeps:    deps.Providers,
		ArtDeps:         deps.Art,
		GameDeps:        deps.Games,
		State:           st,
	}
}

// Ctx returns the per-session base context — the SSH (or WebSocket request)
// context whose Done channel fires on disconnect. Falls back to
// context.Background() when not set (tests that build a Session in-process).
// Prefer this over context.Background() in screen Update handlers so
// fire-and-forget reads exit cleanly when the user disconnects.
func (s *Session) Ctx() context.Context {
	if s.SSHContext != nil {
		return s.SSHContext
	}
	return context.Background()
}

// CtxWithTimeout returns a session-scoped child context that cancels when
// the SSH connection drops OR after timeout elapses, whichever comes first.
// Use in place of context.WithTimeout(context.Background(), …) in screen
// Cmd closures.
func (s *Session) CtxWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.Ctx(), timeout)
}

// WeatherCoords returns the lat/lon/label the Weather screen should query.
// Consults the user's cached primary saved location (if any), falling back
// to the env-var defaults. The cache is loaded at login and refreshed
// whenever the Profile screen mutates the location list.
func (s *Session) WeatherCoords() (lat, lon float64, label string) {
	if s.PrimaryLocation != nil {
		return s.PrimaryLocation.Latitude, s.PrimaryLocation.Longitude, s.PrimaryLocation.Label
	}
	return s.WeatherDefaults.Lat, s.WeatherDefaults.Lon, s.WeatherDefaults.Label
}

// RefreshPrimaryLocation re-reads the user's primary saved location from
// the back end and updates the cached PrimaryLocation. Called from the
// Profile screen after Add/Delete so subsequent WeatherCoords() calls
// reflect the new state without restarting the session.
func (s *Session) RefreshPrimaryLocation(ctx context.Context) error {
	if s.Locations == nil {
		return nil
	}
	loc, err := s.Locations.GetPrimary(ctx, s.Identity.UserID)
	if err != nil {
		return err
	}
	s.PrimaryLocation = loc
	return nil
}
