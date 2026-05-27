// Package session holds the per-SSH-session context passed down to every
// screen. Lives in its own package so screens, the root model, and view
// components can all reference it without a dep cycle.
package session

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/carbonyl"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
)

// artGalleryEntry re-exports art.GalleryEntry via a type alias so the
// session.GalleryEntry name resolves locally for the screens package.
type artGalleryEntry = art.GalleryEntry

// ProfileLocation is the user's .NET-era profile city, sourced from the
// users.location / location_canonical / location_latitude / location_longitude
// columns the .NET stack wrote to. Used as the fallback after PrimaryLocation
// so users carrying data over from .NET don't see "no location" until they've
// added an entry to user_saved_locations. Also fed into
// LocationService.SeedFromProfile at login to backfill an explicit
// saved-location row.
type ProfileLocation struct {
	Label     string
	Canonical string
	Lat       float64
	Lon       float64
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
	// sort_order in user_saved_locations). nil means "no saved location";
	// callers fall through to ProfileLocation, and if that's also nil they
	// must treat the session as having no location at all (WeatherCoords
	// returns ok=false). Loaded once at login by the transport layer;
	// refreshed by the Profile screen after mutating the list.
	PrimaryLocation *realtime.SavedLocation

	// ProfileLocation is the legacy users.location_* fallback, loaded from
	// the user row at login. Consulted by WeatherCoords() after
	// PrimaryLocation so .NET-era profile data still drives the
	// weather/map screens for users who haven't yet added a saved
	// location.
	ProfileLocation *ProfileLocation

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

	// Embedded process-singleton groups: field promotion keeps screen access
	// flat (sess.Pool, sess.Chat, sess.News, ...) even though the underlying
	// structure is grouped.
	CoreDeps
	RealtimeDeps
	ProviderDeps
	ArtDeps
	GameDeps
	SecurityDeps
	SystemDeps

	// Embedded mutable state: same flat-access convenience (sess.Identity,
	// sess.Width, sess.DisplayPrefs, ...).
	State

	// IsSSH is true when the session came from the SSH transport (a real
	// PTY behind the wish.Session), false when it came from the WebSocket
	// bridge (xterm.js, no PTY). Screens gate PTY-dependent features (notably
	// Carbonyl rich-mode) on this — the WS path can't host a child Chromium
	// because there's no /dev/tty to give it.
	IsSSH bool

	// LaunchCarbonyl spawns Carbonyl as a child process attached to the
	// transport's PTY, blocking for the lifetime of the rich-mode session.
	// The transport installs it via AttachSSHLauncher right after constructing
	// the Session — never set on the WS path, so screens must nil-check.
	// Returns once the child exits or the SSH context cancels.
	//
	// The resizes channel is owned by the caller (the screen). It pushes new
	// dimensions whenever it sees a tea.WindowSizeMsg while rich-mode is
	// active — wish's middleware already drains the SSH SIGWINCH chan into
	// WindowSizeMsg, so re-draining at the source would race.
	LaunchCarbonyl func(ctx context.Context, req carbonyl.LaunchRequest, resizes <-chan carbonyl.WinSize) error

	// TeaProgram is the running bubbletea program for this session. Set by
	// the transport (SSH) and the wsbridge (WS) immediately after
	// tea.NewProgram, before Run. Used by the browser screen's rich-mode
	// hand-off to ReleaseTerminal before exec and RestoreTerminal after.
	// Nil during early screen construction; screens calling these methods
	// must nil-check.
	TeaProgram *tea.Program
}

// AttachSSHLauncher wires the Carbonyl launch closure onto an SSH-side session
// after construction. Transport calls this from its programHandler so the
// closure can capture the wish.Session reference (which session.New can't
// see). WS sessions never call this; LaunchCarbonyl stays nil and the screen
// gates accordingly.
func (s *Session) AttachSSHLauncher(launch func(context.Context, carbonyl.LaunchRequest, <-chan carbonyl.WinSize) error) {
	s.IsSSH = true
	s.LaunchCarbonyl = launch
}

// SetTeaProgram is called by both transport (SSH) and wsbridge (WS) right
// after tea.NewProgram and before Run, so screens that need ReleaseTerminal/
// RestoreTerminal can reach it via m.sess.TeaProgram.
func (s *Session) SetTeaProgram(p *tea.Program) {
	s.TeaProgram = p
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
		SSHContext:   sshCtx,
		Graphics:     gfx,
		CoreDeps:     deps.Core,
		RealtimeDeps: deps.Realtime,
		ProviderDeps: deps.Providers,
		ArtDeps:      deps.Art,
		GameDeps:     deps.Games,
		SecurityDeps: deps.Security,
		SystemDeps:   deps.System,
		State:        st,
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
// Resolution order: user's primary saved location → legacy profile city
// (users.location_*). Both are cached at login; the saved-location cache
// is refreshed whenever the Profile screen mutates the list. Returns
// ok=false when neither source is populated — callers must skip the
// weather fetch and surface a "set a location" prompt rather than
// silently picking some default city for the user.
func (s *Session) WeatherCoords() (lat, lon float64, label string, ok bool) {
	if s.PrimaryLocation != nil {
		return s.PrimaryLocation.Latitude, s.PrimaryLocation.Longitude, s.PrimaryLocation.Label, true
	}
	if s.ProfileLocation != nil {
		return s.ProfileLocation.Lat, s.ProfileLocation.Lon, s.ProfileLocation.Label, true
	}
	return 0, 0, "", false
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
