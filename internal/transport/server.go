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
	"github.com/muesli/termenv"
	gossh "golang.org/x/crypto/ssh"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/carbonyl"
	"github.com/nickna/ssh.night.ms/internal/imaging/graphics"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
	"github.com/nickna/ssh.night.ms/internal/security/netlimit"
	"github.com/nickna/ssh.night.ms/internal/tui"
	"github.com/nickna/ssh.night.ms/internal/tui/screens"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

type Config struct {
	Addr        string
	HostKeyDir  string
	IdleTimeout time.Duration

	// MaxAuthTries caps password/pubkey attempts per TCP connection. Mapped
	// to gossh.ServerConfig.MaxAuthTries via the ServerConfigCallback set on
	// the returned *ssh.Server. Zero defers to gossh's default of 6.
	MaxAuthTries int

	// LoginGrace is the wall-clock deadline that fires if the SSH handshake
	// and auth don't complete in time — the in-process equivalent of
	// OpenSSH's LoginGraceTime, defending against slowloris-style handshake
	// stalls. Zero disables. Enforced by wrapping the conn in
	// netlimit.DeadlineConn from the ConnCallback; the deadline is cleared
	// by the auth callback on a successful Known / SignupRequired decision.
	LoginGrace time.Duration

	// HandshakeTracker, when non-nil, enforces a process-wide cap on
	// in-flight unauthenticated handshakes (MaxStartups equivalent). The
	// per-IP gates live on the wrapped net.Listener, not here, because they
	// can reject before any ssh.Context exists.
	HandshakeTracker *netlimit.Tracker

	// Audit, when non-nil, receives HandshakeFailed events from the SSH
	// server's ConnectionFailedCallback and ConnRejectedOverlimit when the
	// HandshakeTracker rejects a connection mid-callback. Both surfaces are
	// invisible to the auth pipeline so without this hook scanners and
	// slowloris-killed conns wouldn't show up anywhere structured.
	Audit audit.Recorder
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

// deadlineConnKey holds the *netlimit.DeadlineConn that wraps the per-conn
// TCP socket so the auth callbacks can clear the handshake deadline and
// release the global unauth-handshake slot on a successful decision.
type deadlineConnKey struct{}

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
			// MiddlewareWithProgramHandler (vs the default Middleware) lets us
			// build the *tea.Program ourselves and stash a reference onto the
			// session so the browser screen can call ReleaseTerminal /
			// RestoreTerminal around the Carbonyl rich-mode exec. Pass
			// termenv.Ascii as the *minimum* color profile floor — clients
			// with richer profiles keep theirs (this matches bm.Middleware's
			// default behavior, just expressed at the explicit constructor).
			bm.MiddlewareWithProgramHandler(programHandler(deps, logger), termenv.Ascii),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		return nil, err
	}

	// MaxAuthTries lives on gossh.ServerConfig, not on ssh.Server — wish has
	// no helper so we install a ServerConfigCallback that returns a minimal
	// config with the cap set. gliderlabs/ssh merges the returned config with
	// its own (pubkey/password callbacks etc.) so it's safe to leave the
	// other fields zero here.
	if cfg.MaxAuthTries > 0 {
		s.ServerConfigCallback = func(ctx ssh.Context) *gossh.ServerConfig {
			return &gossh.ServerConfig{MaxAuthTries: cfg.MaxAuthTries}
		}
	}

	// ConnCallback runs before the SSH handshake. We use it for two things:
	//   1. Acquire a global unauthenticated-handshake slot (MaxStartups
	//      equivalent). If the cap is exceeded, close the conn — the
	//      handshake reader will then error and ConnectionFailedCallback
	//      fires with the reject already attributed.
	//   2. Wrap the conn in a deadline (LoginGraceTime). The auth callback
	//      clears the deadline on a successful Known / SignupRequired
	//      decision; otherwise the deadline fires and the handshake aborts.
	//
	// The wrapper (*netlimit.DeadlineConn) carries an onClose hook that
	// releases the global handshake slot, so a slowloris-killed conn frees
	// its slot at close time even though auth never ran.
	if cfg.HandshakeTracker != nil || cfg.LoginGrace > 0 {
		s.ConnCallback = func(ctx ssh.Context, conn net.Conn) net.Conn {
			var release func()
			if cfg.HandshakeTracker != nil {
				rel, reason, ok := cfg.HandshakeTracker.AcquireHandshake(conn.RemoteAddr())
				if !ok {
					logger.Warn("ssh: handshake rejected by netlimit",
						"remote", conn.RemoteAddr().String(),
						"reason", string(reason))
					if cfg.Audit != nil {
						cfg.Audit.Record(ctx, audit.ConnRejectedOverlimit{
							IP:     netlimit.CollapseIP(conn.RemoteAddr()),
							Reason: string(reason),
						})
					}
					_ = conn.Close()
					return conn
				}
				release = rel
			}
			dc := netlimit.WrapWithDeadline(conn, cfg.LoginGrace, release)
			ctx.SetValue(deadlineConnKey{}, dc)
			return dc
		}
	}

	// ConnectionFailedCallback fires for handshake errors — bad MAC, version-
	// string scanners, protocol downgrade probes, slowloris-killed conns.
	// None of these reach the auth callbacks, so without this hook they're
	// invisible to operators. Routes into both slog (always) and the
	// structured audit recorder (when wired).
	s.ConnectionFailedCallback = func(conn net.Conn, err error) {
		remote := ""
		if conn != nil && conn.RemoteAddr() != nil {
			remote = conn.RemoteAddr().String()
		}
		logger.Warn("ssh: handshake failed", "remote", remote, "err", err.Error())
		if cfg.Audit != nil && conn != nil {
			cfg.Audit.Record(context.Background(), audit.HandshakeFailed{
				IP:  netlimit.CollapseIP(conn.RemoteAddr()),
				Err: err.Error(),
			})
		}
	}

	return &Server{inner: s, logger: logger}, nil
}

func (s *Server) ListenAndServe() error              { return s.inner.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.inner.Shutdown(ctx) }

// ServeWithListener runs the SSH server against a caller-provided listener
// instead of opening one from cfg.Addr. main wraps the raw TCP listener in
// netlimit.Listener to enforce per-IP gates before the SSH handshake starts,
// and routes through this method so the wrap actually takes effect.
func (s *Server) ServeWithListener(l net.Listener) error { return s.inner.Serve(l) }

// clearHandshakeState removes the LoginGraceTime deadline and releases the
// global unauthenticated-handshake slot. Called by the auth callbacks on a
// successful Known / SignupRequired decision. Idempotent thanks to the
// underlying onClose / cleared guards on *netlimit.DeadlineConn.
func clearHandshakeState(ctx ssh.Context) {
	if v := ctx.Value(deadlineConnKey{}); v != nil {
		if dc, ok := v.(*netlimit.DeadlineConn); ok {
			dc.ClearDeadline()
			dc.FireOnClose()
		}
	}
}

func pubKeyHandler(lookup *auth.Lookup, logger *slog.Logger) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		handle := ctx.User()
		fingerprint := gossh.FingerprintSHA256(key)
		algorithm := key.Type()
		blob := key.Marshal()
		ip := ctx.RemoteAddr()

		// Remember the offered key for this session regardless of how the
		// decision goes — used by the password handler if the user falls
		// through to signup.
		ctx.SetValue(offeredKeyKey{}, offeredKey{
			Fingerprint: fingerprint,
			Algorithm:   algorithm,
			Blob:        blob,
		})

		decision := lookup.ByPublicKey(ctx, handle, fingerprint, algorithm, blob, ip)
		ctx.SetValue(authResultKey{}, decision)

		if _, ok := decision.(auth.Known); ok {
			clearHandshakeState(ctx)
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
			clearHandshakeState(ctx)
			logger.Info("password auth ok", "handle", handle)
			return true
		case auth.SignupRequired:
			// Merge any key offered during the pubkey phase so the register
			// screen can surface the adopt-key prompt.
			offered, _ := ctx.Value(offeredKeyKey{}).(offeredKey)
			d = mergeOfferedKey(d, offered)
			ctx.SetValue(authResultKey{}, d)
			clearHandshakeState(ctx)
			logger.Info("password auth → signup", "handle", handle, "has_offered_key", d.OfferedFingerprint != "")
			return true
		}
		ctx.SetValue(authResultKey{}, decision)
		return false
	}
}

// programHandler captures the deps closure and returns the wish/bubbletea
// ProgramHandler callback. Reads the SSH-specific bits off the wish Session,
// hands off to dispatchAuth for model routing, builds the *tea.Program
// ourselves so we can stash a reference + the Carbonyl launcher closure onto
// the constructed session.Session before Run starts.
//
// MiddlewareWithProgramHandler expects a func returning *tea.Program (vs the
// default Middleware which returns (Model, []ProgramOption)). The body here
// mirrors what wish does internally in newDefaultProgramHandler.
func programHandler(deps Deps, logger *slog.Logger) func(ssh.Session) *tea.Program {
	return func(sess ssh.Session) *tea.Program {
		pty, _, active := sess.Pty()
		if !active {
			return nil
		}
		decision := sess.Context().Value(authResultKey{})
		// Detect terminal-image protocol from PTY $TERM + SSH-forwarded
		// environ ($TERM_PROGRAM, $ITERM_SESSION_ID, $NIGHTMS_BROWSER_GRAPHICS
		// override). Halfblock when nothing more capable advertised — safe
		// fallback that works on every SSH client.
		gfx := graphics.Detect(pty.Term, sess.Environ())
		model, opts, s := dispatchAuth(deps.Session, logger, sess.Context(), sess.User(), decision, gfx,
			pty.Window.Width, pty.Window.Height)
		if model == nil {
			return nil
		}
		// MakeOptions wires tea.WithInput/WithOutput onto the wish session
		// the same way bm.Middleware does by default — both emulated and
		// allocated PTYs land on the right io.ReadWriter pair.
		opts = append(opts, bm.MakeOptions(sess)...)
		program := tea.NewProgram(model, opts...)
		if s != nil {
			s.SetTeaProgram(program)
			if deps.Session.System.Carbonyl != nil {
				// Capture sess (wish ssh.Session) + runner reference. The
				// closure is what the browser screen calls; it constructs the
				// per-launch SessionIO around the screen-owned resizes chan
				// and forwards into the Runner.
				runner := deps.Session.System.Carbonyl
				s.AttachSSHLauncher(func(ctx context.Context, req carbonyl.LaunchRequest, resizes <-chan carbonyl.WinSize) error {
					req.SessionRef = newSSHSessionIO(sess, resizes)
					return runner.Launch(ctx, req)
				})
			} else {
				// Runner missing (binary not on disk) — still flag IsSSH so
				// the screen distinguishes WS sessions, but leave the launcher
				// nil so the gate toasts "rich mode unavailable".
				s.IsSSH = true
			}
		}
		return program
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
// Returns (model, opts, sess) where sess is the constructed *session.Session
// (or nil for the early-exit branches: capacity-full and unexpected-decision
// fatal). Callers that need to attach post-construction state (e.g. the
// Carbonyl launcher closure on SSH) check sess != nil before doing so.
func dispatchAuth(
	sdeps session.Deps,
	logger *slog.Logger,
	sshCtx context.Context,
	handle string,
	decision any,
	gfx graphics.Protocol,
	width, height int,
) (tea.Model, []tea.ProgramOption, *session.Session) {
	// Global session cap — applied to both Known and SignupRequired branches
	// (a user on the signup screen still occupies a session-shaped resource).
	// Limit == 0 means unlimited; the runtime-tunable cap is read fresh from
	// the settings cache here so a sysop tightening it during an incident
	// takes effect immediately for new connections.
	var maxTotal int
	if sdeps.Security.Settings != nil {
		maxTotal = sdeps.Security.Settings.Get().MaxTotalSessions
	}
	if !session.AcquireForContext(sshCtx, maxTotal) {
		logger.Warn("session rejected: total session cap reached",
			"handle", handle, "cap", maxTotal, "active", session.ActiveCount())
		return screens.NewMessage("Server is at capacity — please try again shortly."), nil, nil
	}
	switch d := decision.(type) {
	case auth.Known:
		st := session.State{
			Identity: d,
			Width:    width,
			Height:   height,
		}
		// Load the user's primary saved location into the Session cache
		// so WeatherCoords() returns it without an extra DB hit per
		// screen mount. Misses (no row) leave PrimaryLocation nil; if
		// ProfileLocation is also nil the weather/alerts surfaces gate
		// themselves off via WeatherCoords()'s ok=false return.
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
		// The same row carries the legacy location_* fallback that
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
		// are populated (carry-over from an earlier deploy), seed a single
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
		return tui.NewRoot(s), []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}, s
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
			Settings:             sdeps.Security.Settings,
		}, sdeps.Realtime.Presence), []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}, s
	}
	logger.Error("program handler reached with unexpected decision",
		"handle", handle, "decision_type", fmt.Sprintf("%T", decision))
	return screens.NewMessage("internal error: missing auth decision"), nil, nil
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
