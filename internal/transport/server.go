package transport

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	gossh "golang.org/x/crypto/ssh"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/tui"
	"github.com/nickna/ssh.night.ms/internal/tui/screens"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

type Config struct {
	Addr        string
	HostKeyDir  string
	IdleTimeout time.Duration
}

type Server struct {
	inner  *ssh.Server
	logger *slog.Logger
}

// Deps is the bag of process-singletons the transport needs to hand to each
// per-session bubbletea program. Session is the shared per-session
// dependency bag (mirrored by web.Deps.Session); Lookup is consumed by the
// SSH auth callbacks and is transport-only.
type Deps struct {
	Session session.Deps
	Lookup  *auth.Lookup
}

// authResultKey is the ssh.Context value key under which we stash the most-
// recent auth.Decision so the program handler can read it after the handshake.
type authResultKey struct{}

// offeredKeyKey carries the SSH key the client offered during pubkey auth even
// when the decision didn't go that way (e.g., unknown handle → signup). The
// password handler reads this to attach the offered triplet to a SignupRequired
// so the register screen can show the key-adoption checkbox.
type offeredKeyKey struct{}

type offeredKey struct {
	Fingerprint string
	Algorithm   string
	Blob        []byte
}

func NewServer(cfg Config, deps Deps, logger *slog.Logger) (*Server, error) {
	hostKeyPath, err := EnsureHostKey(cfg.HostKeyDir)
	if err != nil {
		return nil, err
	}

	// Wish's middleware emits charmbracelet/log records; let those flow to
	// stderr while our own slog stays the primary logger.
	log.SetReportCaller(false)

	s, err := wish.NewServer(
		wish.WithAddress(cfg.Addr),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithIdleTimeout(cfg.IdleTimeout),
		wish.WithPublicKeyAuth(pubKeyHandler(deps.Lookup, logger)),
		wish.WithPasswordAuth(passwordHandler(deps.Lookup, logger)),
		wish.WithMiddleware(
			bm.Middleware(programHandler(deps, logger)),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		return nil, err
	}
	return &Server{inner: s, logger: logger}, nil
}

func (s *Server) ListenAndServe() error              { return s.inner.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.inner.Shutdown(ctx) }

func pubKeyHandler(lookup *auth.Lookup, logger *slog.Logger) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		handle := ctx.User()
		fingerprint := gossh.FingerprintSHA256(key)
		algorithm := key.Type()
		blob := key.Marshal()

		// Remember the offered key for this session regardless of how the
		// decision goes — used by the password handler if the user falls
		// through to signup.
		ctx.SetValue(offeredKeyKey{}, offeredKey{
			Fingerprint: fingerprint,
			Algorithm:   algorithm,
			Blob:        blob,
		})

		decision := lookup.ByPublicKey(ctx, handle, fingerprint, algorithm, blob)
		ctx.SetValue(authResultKey{}, decision)

		if _, ok := decision.(auth.Known); ok {
			logger.Info("publickey auth ok", "handle", handle, "fp", fingerprint)
			return true
		}
		return false
	}
}

func passwordHandler(lookup *auth.Lookup, logger *slog.Logger) ssh.PasswordHandler {
	return func(ctx ssh.Context, password string) bool {
		handle := ctx.User()
		var ip net.Addr
		if a, ok := ctx.Value(ssh.ContextKeyRemoteAddr).(net.Addr); ok {
			ip = a
		} else {
			ip = ctx.RemoteAddr()
		}

		decision := lookup.ByPassword(ctx, handle, password, ip)

		switch d := decision.(type) {
		case auth.Known:
			ctx.SetValue(authResultKey{}, decision)
			logger.Info("password auth ok", "handle", handle)
			return true
		case auth.SignupRequired:
			// Merge any key offered during the pubkey phase so the register
			// screen can surface the adopt-key prompt.
			offered, _ := ctx.Value(offeredKeyKey{}).(offeredKey)
			d = mergeOfferedKey(d, offered)
			ctx.SetValue(authResultKey{}, d)
			logger.Info("password auth → signup", "handle", handle, "has_offered_key", d.OfferedFingerprint != "")
			return true
		}
		ctx.SetValue(authResultKey{}, decision)
		return false
	}
}

// programHandler captures the deps closure and returns the wish/bubbletea
// program-handler callback. Reads the SSH-specific bits (pty, environ,
// stored decision) off the wish Session, then hands off to dispatchAuth for
// the actual model-routing — that split lets dispatchAuth be tested without
// mocking the wish Session surface.
func programHandler(deps Deps, logger *slog.Logger) func(ssh.Session) (tea.Model, []tea.ProgramOption) {
	return func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
		pty, _, active := sess.Pty()
		if !active {
			return nil, nil
		}
		decision := sess.Context().Value(authResultKey{})
		// Detect terminal-image protocol from PTY $TERM + SSH-forwarded
		// environ ($TERM_PROGRAM, $ITERM_SESSION_ID, $NIGHTMS_BROWSER_GRAPHICS
		// override). Halfblock when nothing more capable advertised — safe
		// fallback that works on every SSH client.
		gfx := graphics.Detect(pty.Term, sess.Environ())
		return dispatchAuth(deps.Session, logger, sess.Context(), sess.User(), decision, gfx,
			pty.Window.Width, pty.Window.Height)
	}
}

// dispatchAuth picks the right tea.Model + ProgramOptions for an auth
// Decision. Pure routing — no SSH dependencies once the caller resolves
// the decision, terminal dims, and graphics protocol. The DB-backed
// per-user prefetches (PrimaryLocation, DisplayPrefs) are nil-skipped so
// tests can pass minimal Deps; production wiring fans every group in.
//
// On Known: prefetch optional prefs into State, build the session, start
// the presence heartbeat goroutine bound to sshCtx, return the lobby Root.
// On SignupRequired: build a state-light session and return the signup Root
// (heartbeat fires later from registerCompletedMsg). Any other Decision is
// an internal invariant violation — wish should never have admitted the
// session — so we return a fatal-message model rather than panic.
func dispatchAuth(
	sdeps session.Deps,
	logger *slog.Logger,
	sshCtx context.Context,
	handle string,
	decision any,
	gfx graphics.Protocol,
	width, height int,
) (tea.Model, []tea.ProgramOption) {
	switch d := decision.(type) {
	case auth.Known:
		st := session.State{
			Identity: d,
			Width:    width,
			Height:   height,
		}
		// Load the user's primary saved location into the Session cache
		// so WeatherCoords() returns it without an extra DB hit per
		// screen mount. Misses (no row) leave PrimaryLocation nil and
		// fall back to WeatherDefaults.
		if sdeps.Realtime.Locations != nil {
			loadCtx, cancel := context.WithTimeout(sshCtx, 2*time.Second)
			if loc, err := sdeps.Realtime.Locations.GetPrimary(loadCtx, d.UserID); err != nil {
				logger.Warn("primary location load", "handle", d.Handle, "err", err)
			} else {
				st.PrimaryLocation = loc
			}
			cancel()
		}
		// Cache the user's display prefs (tz / clock / date format) so
		// the status-bar clock + every other render-time formatter can
		// stay synchronous. A load failure leaves the zero value, which
		// renders as UTC + ISO + 24-hour — same as a brand-new row.
		// The same row carries the .NET-era location_* fallback that
		// WeatherCoords() consults when the user has no saved location.
		if sdeps.Core.Queries != nil {
			loadCtx, cancel := context.WithTimeout(sshCtx, 2*time.Second)
			if user, err := sdeps.Core.Queries.GetUserByID(loadCtx, d.UserID); err != nil {
				logger.Warn("display prefs load", "handle", d.Handle, "err", err)
			} else {
				st.DisplayPrefs = session.DisplayPrefsFromUser(user)
				st.ProfileLocation = session.ProfileLocationFromUser(user)
			}
			cancel()
		}
		// One-shot backfill: when the user has nothing in
		// user_saved_locations but the legacy users.location_* columns
		// are populated (carry-over from the .NET stack), seed a single
		// row so the Profile screen's saved-locations list and
		// WeatherCoords() converge on the same source of truth. After
		// this point the read-through fallback in WeatherCoords() is a
		// safety net rather than the active path.
		if st.PrimaryLocation == nil && st.ProfileLocation != nil && sdeps.Realtime.Locations != nil {
			seedCtx, cancel := context.WithTimeout(sshCtx, 2*time.Second)
			if seeded, err := sdeps.Realtime.Locations.SeedFromProfile(seedCtx, d.UserID,
				st.ProfileLocation.Label, st.ProfileLocation.Canonical,
				st.ProfileLocation.Lat, st.ProfileLocation.Lon); err != nil {
				logger.Warn("profile-location backfill", "handle", d.Handle, "err", err)
			} else if seeded != nil {
				st.PrimaryLocation = seeded
			}
			cancel()
		}
		s := session.New(sdeps, st, sshCtx, gfx)
		if sdeps.Realtime.Presence != nil {
			go sdeps.Realtime.Presence.RunHeartbeat(sshCtx, d.Handle, d.UserID)
		}
		return tui.NewRoot(s), []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	case auth.SignupRequired:
		st := session.State{
			Width:  width,
			Height: height,
		}
		s := session.New(sdeps, st, sshCtx, gfx)
		// Heartbeat doesn't start until registration completes; the Root
		// model fires it from the registerCompletedMsg handler.
		return tui.NewSignupRoot(s, d, auth.RegisterDeps{
			Pool:                 sdeps.Core.Pool,
			Hasher:               sdeps.Core.Hasher,
			BootstrapSysopHandle: sdeps.Policy.BootstrapSysopHandle,
			MinPasswordLength:    sdeps.Policy.MinPasswordLength,
		}, sdeps.Realtime.Presence), []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	}
	logger.Error("program handler reached with unexpected decision",
		"handle", handle, "decision_type", fmt.Sprintf("%T", decision))
	return screens.NewMessage("internal error: missing auth decision"), nil
}

// mergeOfferedKey copies an SSH pubkey that was offered but not used for
// authentication onto a SignupRequired decision so the register screen can
// pre-populate the key-adoption checkbox. Returns the (possibly modified)
// decision. Empty fingerprint = no key was offered; in that case the
// returned value equals the input.
//
// Extracted from passwordHandler so the merge logic can be unit-tested
// without mocking the full wish SSH context interface.
func mergeOfferedKey(d auth.SignupRequired, offered offeredKey) auth.SignupRequired {
	if offered.Fingerprint == "" {
		return d
	}
	d.OfferedFingerprint = offered.Fingerprint
	d.OfferedAlgorithm = offered.Algorithm
	d.OfferedBlob = offered.Blob
	return d
}
