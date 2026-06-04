// Package web is the HTTP surface of nightms: landing page, login + OAuth,
// profile/account pages, and the /ws/bbs WebSocket SSH bridge that runs the
// same bubbletea program as the SSH path. Templates and static assets are
// embedded so the binary stays drop-anywhere.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/csrf"
	"github.com/redis/go-redis/v9"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Config is the web-server slice of process configuration.
type Config struct {
	Addr       string // ":5080"
	PublicHost string // web origin used for CSRF trusted origins, WS Origin checks (e.g. "k.night.ms")
	// SSHHost is the host string rendered into "ssh -p 2222 you@<host>"
	// snippets in the templates. Allowed to differ from PublicHost so an
	// operator running SSH on a direct DNS name and HTTP behind Cloudflare
	// can advertise the correct connection string. Falls back to PublicHost
	// when empty.
	SSHHost string
	// SSHPort is the externally-reachable SSH port shown alongside SSHHost.
	// Defaults to the bind port (parsed from Addr) when empty; when set to
	// "22" the templates omit the -p flag entirely.
	SSHPort        string
	CookieSecret   []byte // ≥ 32 bytes; used by cookie sign + CSRF middleware
	SecureCookies  bool   // set Secure flag on cookies (true behind TLS)
	SessionTimeout time.Duration
	PFPDir         string // profile picture storage directory
}

// Deps groups the runtime collaborators handed to handlers via the request
// context. Session is the same per-session bag the SSH transport hands to
// session.New() — keeping the two on a single source of truth means the
// /ws/bbs bridge runs every screen with the same providers and services
// the SSH path does. CoreDeps and RealtimeDeps are also embedded for
// handler-call-site ergonomics: h.deps.Pool, h.deps.Queries, h.deps.Chat
// all resolve via field promotion. Use web.NewDeps to construct so the
// embedded views stay in sync with Session.
type Deps struct {
	// Session is the process-singleton bag fed into session.New() when the
	// /ws/bbs bridge spins up a tea.Program for a logged-in user. Mirrors
	// transport.Deps.Session.
	Session session.Deps

	// Embedded views of Session.Core and Session.Realtime — promoted field
	// access (h.deps.Pool, h.deps.Queries, h.deps.Logger, h.deps.Chat,
	// h.deps.Presence) keeps handler code tight. main.go should build Deps
	// via web.NewDeps, which mirrors Session into these embeds.
	session.CoreDeps
	session.RealtimeDeps

	// Lookup is consumed by the web login form (POST /login). Web-only.
	Lookup *auth.Lookup

	// Redis backs the server-side session store. Required: web sessions
	// are server-state-anchored now (no more HMAC-only cookies) so the
	// web side cannot start without Redis. SSH uses the same client for
	// rate limits + pub/sub.
	Redis *redis.Client

	// OAuth providers — either field may be nil if its env vars aren't set.
	// The login template hides buttons for unconfigured providers; OAuth
	// routes return 404 for them.
	OAuth OAuthProviders

	// Audit is the structured-event recorder. Used by the OAuth link /
	// unlink handlers to surface user-driven account changes in the sysop
	// events feed. Nil-safe — callers check before recording.
	Audit audit.Recorder
}

// NewDeps assembles a Deps from the canonical session.Deps and the web-only
// extras (Lookup, Redis, OAuth). Mirrors the Session.Core / Session.Realtime
// into the embedded views so handler call sites can stay on the short field
// names (h.deps.Pool, h.deps.Chat) without main.go having to set them twice.
func NewDeps(s session.Deps, lookup *auth.Lookup, redis *redis.Client, oauth OAuthProviders, auditRec audit.Recorder) Deps {
	return Deps{
		Session:      s,
		CoreDeps:     s.Core,
		RealtimeDeps: s.Realtime,
		Lookup:       lookup,
		Redis:        redis,
		OAuth:        oauth,
		Audit:        auditRec,
	}
}

// Server wraps the http.Server so cmd/nightms can call Listen + Shutdown
// without taking a direct dependency on net/http.
type Server struct {
	inner  *http.Server
	logger *slog.Logger
}

// NewServer builds the router and wires templates + middleware. ListenAndServe
// returns when the listener stops.
func NewServer(cfg Config, deps Deps) (*Server, error) {
	if len(cfg.CookieSecret) < 32 {
		return nil, fmt.Errorf("web: cookie secret must be at least 32 bytes")
	}
	if deps.Redis == nil {
		return nil, fmt.Errorf("web: redis client required for session store")
	}
	if cfg.SSHHost == "" {
		cfg.SSHHost = cfg.PublicHost
	}
	if cfg.SSHPort == "" {
		// Bind-port fallback so the displayed command matches the dev port
		// when the operator hasn't set NIGHTMS_SSH_PORT explicitly.
		cfg.SSHPort = portFromAddr(cfg.Addr)
	}
	tpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static sub: %w", err)
	}

	sessions := newSessionStore(deps.Redis, cfg.SecureCookies, cfg.SessionTimeout)
	h := &handlers{
		deps:      deps,
		cfg:       cfg,
		templates: tpl,
		sessions:  sessions,
		oauth:     deps.OAuth,
	}

	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(smallBodyLimit)
	r.Use(h.attachIdentity)
	// middleware.Timeout is NOT applied at the root because /ws/bbs is a
	// long-lived bubbletea session, not a 30-second request. It's installed
	// inside the timedGroup below, which scopes it to the regular HTTP
	// surface and leaves the WebSocket bridge unconstrained.

	// gorilla/csrf v1.7+ assumes HTTPS by default and enforces Origin / Referer
	// matching strictly. For dev (and TLS-terminated-at-proxy deployments) we
	// flag the request as plaintext via the PlaintextHTTPContextKey so the
	// strict checks relax to what they were in v1.6 and earlier. PublicHost
	// + localhost are still trusted via csrf.TrustedOrigins below.
	if !cfg.SecureCookies {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, csrf.PlaintextHTTPRequest(r))
			})
		})
	}

	// CSRF must come AFTER attachIdentity so the cookie can be loaded first.
	// TrustedOrigins is needed because gorilla/csrf v1.7+ enforces origin
	// matching on POST regardless of scheme — without it, curl/PowerShell
	// requests against the dev port fail with "referer invalid". PublicHost
	// covers the prod hostname; the localhost entries make dev smoke tests
	// work over plain HTTP without juggling Referer headers.
	csrfMiddleware := csrf.Protect(
		cfg.CookieSecret,
		csrf.Secure(cfg.SecureCookies),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteLaxMode),
		csrf.TrustedOrigins([]string{
			cfg.PublicHost,
			"localhost",
			"localhost:" + portFromAddr(cfg.Addr),
			"127.0.0.1",
			"127.0.0.1:" + portFromAddr(cfg.Addr),
		}),
	)
	// /ws/bbs intentionally sits OUTSIDE both the CSRF and the 30s Timeout
	// group below. WebSocket upgrades authenticate via the session cookie
	// (loaded by attachIdentity above) and gorilla/csrf would otherwise
	// insist on a token in the GET that initiates the upgrade. The Timeout
	// middleware is even more disqualifying: it wraps r.Context() with a
	// 30s deadline, which would tear the bubbletea session down mid-keystroke.
	// Browser same-origin policy + the Origin check in coder/websocket cover
	// the CSRF angle.
	r.Get("/ws/bbs", h.handleBBSWebSocket)

	// /chat/{id}/stream is the chat Server-Sent Events feed. Like /ws/bbs it
	// sits OUTSIDE the 30s Timeout group — the stream is long-lived, and a
	// request-context deadline would tear it down every 30 seconds. It
	// authenticates via the session cookie loaded by attachIdentity above;
	// CSRF is irrelevant for a GET. The companion read/write routes that DO
	// belong under the normal timeout live in the csrf group below.
	r.Get("/chat/{channelID}/stream", h.chatStream)

	// Everything else gets the 30s request-context Timeout. Static-file
	// serves, the healthz probe, and the avatar/CSRF routes all complete
	// well inside that budget.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(30 * time.Second))

		r.Group(func(r chi.Router) {
			r.Use(csrfMiddleware)
			r.Get("/", h.landing)
			r.Get("/login", h.loginGet)
			r.Post("/login", h.loginPost)
			r.Post("/logout", h.logoutPost)
			r.Post("/logout-all", h.logoutAllPost)
			r.Get("/profile/sessions", h.sessionsView)
			r.Post("/profile/sessions/{sid}/revoke", h.sessionsRevoke)
			r.Get("/terminal", h.terminalPage)
			r.Get("/profile", h.profileView)
			r.Get("/profile/password", h.passwordGet)
			r.Post("/profile/password", h.passwordPost)
			r.Get("/profile/keys", h.keysGet)
			r.Post("/profile/keys", h.keysAdd)
			r.Post("/profile/keys/{id}/delete", h.keysDelete)
			r.Get("/profile/picture", h.pictureGet)
			r.Post("/profile/picture", h.picturePost)
			r.Post("/profile/picture/clear", h.pictureClear)
			r.Get("/u/{handle}", h.publicProfile)

			// Boards — server-rendered forum. Reads are public; the POST
			// handlers gate on a session and redirect anonymous users to
			// /login. The static "/new" segment is registered before the
			// "{topicID}" wildcard, and chi prioritizes static routes over
			// params regardless, so /boards/{id}/new never resolves as a topic.
			r.Get("/boards", h.boardsIndex)
			r.Get("/boards/{forumID}", h.boardForum)
			r.Get("/boards/{forumID}/new", h.boardNewGet)
			r.Post("/boards/{forumID}/new", h.boardNewPost)
			r.Get("/boards/{forumID}/{topicID}", h.boardTopic)
			r.Post("/boards/{forumID}/{topicID}/reply", h.boardReplyPost)

			// Chat — server-rendered live chat. Every route requires a session
			// (the handlers redirect anonymous users to /login). Reuses the same
			// realtime.ChatService as the SSH/TUI path, so messages cross both
			// surfaces. The live feed (/chat/{id}/stream) is registered above,
			// outside the Timeout group. Static "/chat/join" and "/chat/dm" are
			// declared before the "{channelID}" wildcard; chi prioritizes static
			// segments regardless, so they never resolve as a channel id.
			r.Get("/chat", h.chatIndex)
			r.Post("/chat/join", h.chatJoin)
			r.Post("/chat/dm", h.chatStartDM)
			r.Get("/chat/{channelID}", h.chatChannel)
			r.Post("/chat/{channelID}/send", h.chatSend)
			// Per-message actions, fired by chat.js with fetch() (CSRF token in
			// the X-CSRF-Token header). They return 204; the resulting Redis
			// event patches every open client over SSE.
			r.Post("/chat/{channelID}/react", h.chatReact)
			r.Post("/chat/{channelID}/unreact", h.chatUnreact)
			r.Post("/chat/{channelID}/pin", h.chatPin)
			r.Post("/chat/{channelID}/edit", h.chatEdit)
			r.Post("/chat/{channelID}/delete", h.chatDelete)

			// Weather — server-rendered forecast for the signed-in user's
			// primary saved location. Login-gated (the handler redirects
			// anonymous requests to /login); reuses the same Open-Meteo + NWS
			// providers and DisplayPrefs formatters as the SSH/TUI screen.
			r.Get("/weather", h.weatherIndex)

			r.Get("/profile/connections", h.connectionsView)
			r.Post("/profile/connections/{id}/unlink", h.connectionsUnlink)
			r.Get("/profile/rename", h.renameGet)
			r.Post("/profile/rename", h.renamePost)

			// OAuth account-linking routes. /auth/{provider}/start requires an
			// active session and kicks the link dance; /callback validates state
			// and attaches the credential to the signed-in user. There is no
			// "log in with Google" path — OAuth is for linking Gmail/Drive/etc.
			// access to an existing SSH account.
			r.Get("/auth/{provider}/start", h.oauthStart)
			r.Get("/auth/{provider}/callback", h.oauthCallback)
		})

		// /u/{handle}/avatar.png is outside the CSRF group — it's a GET image
		// endpoint with no state mutation, and we'd rather not pay the cookie
		// dance on every <img src> request from third-party renderers.
		r.Get("/u/{handle}/avatar", h.avatar)

		// /healthz backs the Docker HEALTHCHECK. Plain 200/"ok" — checking DB
		// or Redis here would couple container liveness to dependencies and
		// flap the status on transient hiccups.
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok\n"))
		})

		r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return &Server{inner: srv, logger: deps.Logger}, nil
}

func (s *Server) ListenAndServe() error {
	err := s.inner.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error { return s.inner.Shutdown(ctx) }

// parseTemplates parses every *.html.tmpl from the embedded FS into a single
// template set. Each page template defines a "body" block consumed by the
// shared layout.
func parseTemplates() (map[string]*template.Template, error) {
	out := map[string]*template.Template{}
	layoutBytes, err := templateFS.ReadFile("templates/layout.html.tmpl")
	if err != nil {
		return nil, err
	}
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "layout.html.tmpl" {
			continue
		}
		t := template.New("layout")
		if _, err := t.Parse(string(layoutBytes)); err != nil {
			return nil, fmt.Errorf("parse layout: %w", err)
		}
		body, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			return nil, err
		}
		if _, err := t.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		// Trim ".html.tmpl" → page key (e.g. "landing", "login").
		key := e.Name()
		if i := indexFold(key, "."); i > 0 {
			key = key[:i]
		}
		out[key] = t
	}
	return out, nil
}

// portFromAddr returns the port substring of a ":port" or "host:port" Addr
// — used to feed gorilla/csrf's TrustedOrigins list without hardcoding a
// default. Returns "" if the address has no port.
func portFromAddr(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
}

// smallBodyLimit caps inbound request bodies at 1 MiB on mutating methods —
// defense-in-depth against an attacker uploading multi-gigabyte POSTs to
// exhaust memory. The profile-picture upload sets its own larger
// (5 MiB) MaxBytesReader inside the handler and is skipped here; reaching
// that handler still works because the second wrap would otherwise clip
// the body before the multipart parse.
func smallBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.URL.Path != "/profile/picture" {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func indexFold(s, sep string) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
