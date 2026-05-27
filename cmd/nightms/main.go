package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on http.DefaultServeMux when DebugAddr is set
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/muesli/termenv"
	"github.com/redis/go-redis/v9"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/devicecode"
	"github.com/nickna/ssh.night.ms/internal/auth/oauthrefresh"
	"github.com/nickna/ssh.night.ms/internal/auth/tokenseal"
	"github.com/nickna/ssh.night.ms/internal/carbonyl"
	"github.com/nickna/ssh.night.ms/internal/config"
	"github.com/nickna/ssh.night.ms/internal/data"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/holdem/multiplayer"
	roulettemp "github.com/nickna/ssh.night.ms/internal/doors/roulette/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/imaging/asyncfetch"
	"github.com/nickna/ssh.night.ms/internal/providers/finance"
	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/providers/maptile"
	"github.com/nickna/ssh.night.ms/internal/providers/news"
	"github.com/nickna/ssh.night.ms/internal/providers/routing"
	"github.com/nickna/ssh.night.ms/internal/providers/weather"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
	"github.com/nickna/ssh.night.ms/internal/security/netlimit"
	"github.com/nickna/ssh.night.ms/internal/settings"
	"github.com/nickna/ssh.night.ms/internal/transport"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/web"

	"golang.org/x/time/rate"
)

// Startup ordering is load-bearing:
//
//  1. Migrations  — must finish before any pool reads
//  2. Pool        — single *pgxpool.Pool
//  3. Seed        — creates #lobby etc. before sysop bootstrap
//  4. Sysop       — may need #lobby to exist
//  5. Redis       — backs presence, rate limit, sessions
//  6. Services    — realtime + providers + art + games + policy
//  7. SSH         — transport listener
//  8. HTTP        — web listener
//
// Each phase is its own function below; main() reads as a table of contents.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		os.Exit(runHealthcheck())
	}
	// In a scratch container TERM is unset, so lipgloss's package-level
	// renderer falls to the Ascii profile and strips all color. Force
	// ANSI256 when there's no TTY so SSH clients see the BBS in color;
	// dev runs (with a real $TERM) keep autodetection.
	if os.Getenv("TERM") == "" {
		lipgloss.SetColorProfile(termenv.ANSI256)
	}
	opts := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: opts.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Optional pprof + /debug/vars listener — bound to whatever address the
	// operator chose, intended for localhost or an internal network only.
	// Off by default; never expose to the public internet.
	if opts.DebugAddr != "" {
		go func() {
			logger.Info("debug listener starting", "addr", opts.DebugAddr)
			if err := http.ListenAndServe(opts.DebugAddr, nil); err != nil && err != http.ErrServerClosed {
				logger.Warn("debug listener exited", "err", err)
			}
		}()
	}

	mustMigrate(ctx, opts, logger)
	pool := mustOpenPool(ctx, opts, logger)
	defer pool.Close()
	queries := gen.New(pool)
	hasher := auth.NewHasher(opts.Argon2Params)
	mustSeed(ctx, queries, logger)
	mustBootstrapSysop(ctx, pool, hasher, opts, logger)
	redisClient := mustOpenRedis(ctx, opts, logger)
	defer redisClient.Close()

	// Security audit recorder (Phase C): writes structured events to both
	// stderr (slog JSON, always synchronous) and Postgres (security_events,
	// best-effort via a buffered channel). Constructed before everything
	// that emits events so the wiring is purely additive.
	auditRecorder := audit.NewPostgresRecorder(queries, logger.With("component", "audit"), 2048)
	go auditRecorder.Run(ctx)

	// Sysop-tunable runtime settings (signups on/off, MOTD, connection caps,
	// lockout thresholds). KV table in Postgres, in-process Snapshot, Redis
	// pub/sub push-invalidation across replicas. Defaults are sourced from
	// config.Options so a brand-new install behaves identically to the
	// pre-settings build.
	settingsCache := settings.NewCache(queries, redisClient, logger.With("component", "settings"), defaultsFromOptions(opts), 30*time.Second)
	if err := settingsCache.Load(ctx); err != nil {
		logger.Error("settings initial load", "err", err)
		os.Exit(1)
	}
	go settingsCache.Run(ctx)

	sessionDeps, holdemReg, rouletteReg := buildSessionDeps(ctx, pool, queries, hasher, redisClient, opts, logger)

	// Persistent-ban cache (Phase B): in-process map of active security_ip_bans
	// rows, refreshed every 30s and push-invalidated via Redis pub/sub. Load
	// synchronously before the listener starts so the first auth attempt
	// sees the persisted bans. Run() drives the refresh + listen loops; it
	// exits cleanly when ctx cancels.
	banCache := auth.NewBanCache(queries, redisClient, logger.With("component", "ban-cache"), 30*time.Second)
	banCache.Audit = auditRecorder
	if err := banCache.Load(ctx); err != nil {
		logger.Error("ban cache initial load", "err", err)
		os.Exit(1)
	}
	go banCache.Run(ctx)

	rateLimiter := auth.NewRedisRateLimiter(redisClient, opts.RateLimit, logger.With("component", "rate-limit"))
	rateLimiter.Audit = auditRecorder
	rateLimiter.Settings = settingsCache
	// When the per-IP lockcount crosses the persistent-ban threshold, write
	// a security_ip_bans row and broadcast pub/sub so every replica's
	// BanCache picks it up. Uses context.Background — the auth ctx that
	// triggered the lockout may cancel as soon as the conn closes, but the
	// ban write must persist regardless.
	rateLimiter.OnPersistentBanEligible = func(ip string, lockcount int64) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		reason := fmt.Sprintf("auto: %d lockouts within window", lockcount)
		if err := banCache.AddBan(bgCtx, ip, opts.PersistentBanDuration, reason, "auto"); err != nil {
			logger.Warn("persistent ban auto-promote", "ip", ip, "lockcount", lockcount, "err", err)
		} else {
			logger.Info("persistent ban auto-promote",
				"ip", ip, "lockcount", lockcount, "duration", opts.PersistentBanDuration)
		}
	}

	lookup := &auth.Lookup{
		Pool:    pool,
		Queries: queries,
		Hasher:  hasher,
		Limiter: rateLimiter,
		Logger:  logger.With("component", "auth"),
		Bans:    banCache,
		Audit:   auditRecorder,
	}

	attachSecurity(&sessionDeps, banCache, settingsCache)

	// OAuth: token-encryption sealer + device-code service + background
	// refresher. The sealer uses NIGHTMS_OAUTH_TOKEN_SECRET if set, else
	// HKDF-derives from the cookie secret (which means rotating the cookie
	// secret invalidates every stored OAuth token unless the operator also
	// supplies NIGHTMS_OAUTH_TOKEN_SECRET).
	oauthMasterKey := opts.OAuthTokenSecret
	if len(oauthMasterKey) == 0 {
		oauthMasterKey = opts.WebCookieSecret
	}
	oauthSealer, err := tokenseal.New(oauthMasterKey)
	if err != nil {
		logger.Error("oauth sealer init", "err", err)
		os.Exit(1)
	}

	// Device-flow providers. Google needs a *separate* "TVs and Limited
	// Input devices" OAuth client because its device endpoint rejects
	// regular Web-application clients with disabled_client. Microsoft
	// reuses the same client (must have "Allow public client flows" =
	// true in Azure app registration). If a provider's device client
	// isn't configured the corresponding TUI branch surfaces "unavailable"
	// to the user.
	deviceProviders := map[auth.OAuthProviderKind]*auth.OAuthProvider{}
	if opts.GoogleDeviceClientID != "" {
		deviceProviders[auth.OAuthGoogle] = auth.NewGoogleProvider(
			opts.GoogleDeviceClientID, opts.GoogleDeviceClientSecret, "")
	}
	if opts.MicrosoftClientID != "" {
		deviceProviders[auth.OAuthMicrosoft] = auth.NewMicrosoftProvider(
			opts.MicrosoftClientID, opts.MicrosoftClientSecret, "")
	}
	if len(deviceProviders) > 0 {
		deviceSvc := &devicecode.Service{
			Pool:      devicecode.PoolAdapter{Pool: pool, Queries: queries},
			Queries:   queries,
			Redis:     redisClient,
			Sealer:    oauthSealer,
			Providers: deviceProviders,
			Audit:     auditRecorder,
			Logger:    logger.With("component", "oauth-device"),
		}
		sessionDeps.Core.OAuthDeviceCode = deviceSvc
	}
	sessionDeps.Core.OAuthSealer = oauthSealer

	// Background refresher. Renews access tokens before they expire so
	// future Gmail/Drive/Outlook/OneDrive integrations don't 401. Soft-
	// failure threshold is 5 ticks (matches the package default).
	// Same provider map as the device flow, since refresh uses
	// client_id/client_secret + refresh_token regardless of how the
	// initial token was obtained.
	if len(deviceProviders) > 0 {
		refresher, err := oauthrefresh.New(oauthrefresh.Config{
			Queries:   queries,
			Sealer:    oauthSealer,
			Providers: deviceProviders,
			Audit:     auditRecorder,
			Logger:    logger.With("component", "oauth-refresh"),
			Interval:  opts.OAuthRefreshInterval,
			LeadTime:  opts.OAuthRefreshLeadTime,
			BatchSize: opts.OAuthRefreshBatchSize,
			Workers:   opts.OAuthRefreshWorkers,
		})
		if err != nil {
			logger.Error("oauth refresher init", "err", err)
			os.Exit(1)
		}
		go refresher.Run(ctx)
	}

	// Carbonyl "rich mode" browser runner. Process singleton — Launch fans
	// out per-session children. Returns (nil, ErrBinaryMissing) when the
	// binary isn't on disk; that's the soft-disable path so the BBS still
	// boots without the bundle (e.g., local dev without `make
	// carbonyl-bundle`). When nil, the browser screen's R hotkey toasts
	// "rich mode unavailable".
	carbonylInitial := carbonyl.Limits{
		Global:    opts.Carbonyl.MaxGlobal,
		PerIP:     opts.Carbonyl.MaxPerIP,
		PerHandle: opts.Carbonyl.MaxPerHandle,
	}
	carbonylRunner, err := carbonyl.New(carbonyl.Config{
		BinPath:  opts.Carbonyl.BinPath,
		DataRoot: opts.Carbonyl.DataDir,
		Logger:   logger.With("component", "carbonyl"),
		Limits:   carbonylInitial,
	})
	if err != nil {
		logger.Warn("carbonyl: rich mode disabled", "err", err)
	} else {
		sessionDeps.System.Carbonyl = carbonylRunner
		// Push updated caps on every settings refresh. Carbonyl-side gates
		// only matter when the binary is actually present, so the hook is
		// inside the success branch.
		settingsCache.OnChange(func(snap settings.Snapshot) {
			carbonylRunner.UpdateLimits(carbonyl.Limits{
				Global:    snap.CarbonylMaxGlobal,
				PerIP:     snap.CarbonylMaxPerIP,
				PerHandle: snap.CarbonylMaxPerHandle,
			})
		})
	}

	// SSH-listener netlimit tracker — bounds per-IP concurrent conns + new-conn
	// rate, plus the global in-flight unauth-handshake cap. The per-IP gates
	// are enforced by wrapping the net.Listener; the global cap is enforced
	// inside the transport's ConnCallback (because it needs ssh.Context to
	// release on auth-success). Run() drives the idle-entry GC loop.
	//
	// OnReject is wired to the audit recorder so per-IP cap / token-bucket
	// rejections surface in the structured events feed alongside the
	// matching slog line.
	sshTracker := netlimit.NewTracker(netlimit.Config{
		MaxConnPerIP:        opts.SSHSecurity.MaxConnPerIP,
		PerIPRate:           rate.Limit(opts.SSHSecurity.PerIPConnRate),
		PerIPBurst:          opts.SSHSecurity.PerIPConnBurst,
		MaxUnauthHandshakes: opts.SSHSecurity.MaxUnauthHandshakes,
	}, logger.With("component", "netlimit"), func(addr net.Addr, reason netlimit.RejectReason) {
		auditRecorder.Record(context.Background(), audit.ConnRejectedOverlimit{
			IP:     netlimit.CollapseIP(addr),
			Reason: string(reason),
		})
	})
	go sshTracker.Run(ctx)

	// Push settings.Snapshot changes into the netlimit tracker on every
	// refresh. OnChange fires synchronously once with the current snapshot
	// (sync the env-derived initial state) and again on every later refresh.
	// The per-IP rate/burst keep their env-driven values — the catalog only
	// exposes max_conn_per_ip + max_unauth_handshakes, which are the two an
	// operator might want to tighten during an active incident.
	settingsCache.OnChange(func(snap settings.Snapshot) {
		sshTracker.UpdateConfig(netlimit.Config{
			MaxConnPerIP:        snap.MaxConnPerIP,
			PerIPRate:           rate.Limit(opts.SSHSecurity.PerIPConnRate),
			PerIPBurst:          opts.SSHSecurity.PerIPConnBurst,
			MaxUnauthHandshakes: snap.MaxUnauthHandshakes,
		})
	})

	srv, err := transport.NewServer(transport.Config{
		Addr:             opts.SSHAddr,
		HostKeyDir:       opts.HostKeyDir,
		IdleTimeout:      opts.IdleTimeout,
		MaxAuthTries:     opts.SSHSecurity.MaxAuthTries,
		LoginGrace:       opts.SSHSecurity.LoginGrace,
		HandshakeTracker: sshTracker,
		Audit:            auditRecorder,
	}, transport.Deps{
		Session: sessionDeps,
		Lookup:  lookup,
	}, logger.With("component", "ssh"))
	if err != nil {
		logger.Error("transport init", "err", err)
		os.Exit(1)
	}

	// Open the SSH socket here (rather than letting wish's ListenAndServe do
	// it) so we can wrap it in the netlimit.Listener. The per-IP cap +
	// token bucket reject conns before any handshake bytes are read.
	sshListener, err := net.Listen("tcp", opts.SSHAddr)
	if err != nil {
		logger.Error("ssh listen", "addr", opts.SSHAddr, "err", err)
		os.Exit(1)
	}
	sshListener = netlimit.NewListener(sshListener, sshTracker)

	webSrv, err := web.NewServer(web.Config{
		Addr:           opts.HTTPAddr,
		PublicHost:     opts.WebPublicHost,
		SSHHost:        opts.WebSSHHost,
		CookieSecret:   opts.WebCookieSecret,
		SecureCookies:  opts.WebSecureCookies,
		SessionTimeout: 30 * 24 * time.Hour,
		PFPDir:         opts.PFPDir,
	}, web.NewDeps(sessionDeps, lookup, redisClient, buildOAuthProviders(opts), auditRecorder))
	if err != nil {
		logger.Error("web init", "err", err)
		os.Exit(1)
	}

	logger.Info("nightms starting",
		"ssh_addr", opts.SSHAddr,
		"http_addr", opts.HTTPAddr,
		"host_key_dir", opts.HostKeyDir,
		"redis", opts.RedisConnStr,
		"ssh_max_auth_tries", opts.SSHSecurity.MaxAuthTries,
		"ssh_login_grace", opts.SSHSecurity.LoginGrace,
		"ssh_max_conn_per_ip", opts.SSHSecurity.MaxConnPerIP)

	runListeners(ctx, srv, sshListener, webSrv, holdemReg, rouletteReg, logger)
}

// runHealthcheck backs the Docker HEALTHCHECK in deploy/compose.yml. The
// scratch runtime image has no shell, curl, or wget, so the probe has to run
// inside the binary itself. Reads BBS_HTTP_PORT to mirror the listener's bind.
func runHealthcheck() int {
	port := os.Getenv("BBS_HTTP_PORT")
	if port == "" {
		port = "5080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

// mustMigrate runs the schema migrations synchronously. Failure terminates
// the process — we don't start an SSH listener against a half-migrated DB.
func mustMigrate(ctx context.Context, opts config.Options, logger *slog.Logger) {
	migrateCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := data.RunMigrations(migrateCtx, opts.DBConnStr, logger); err != nil {
		logger.Error("migrations failed", "err", err)
		os.Exit(1)
	}
}

// mustOpenPool parses the connection string and returns a connected pool.
// Pings the DB before handing the pool back so a misconfigured connstr
// surfaces as a startup error, not a first-request error.
func mustOpenPool(ctx context.Context, opts config.Options, logger *slog.Logger) *pgxpool.Pool {
	poolCfg, err := pgxpool.ParseConfig(opts.DBConnStr)
	if err != nil {
		logger.Error("pgxpool parse", "err", err)
		os.Exit(1)
	}
	if opts.DBMaxConns > 0 {
		poolCfg.MaxConns = opts.DBMaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		logger.Error("pgxpool connect", "err", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		logger.Error("pgxpool ping", "err", err)
		os.Exit(1)
	}
	return pool
}

// mustSeed inserts the default rows (#lobby etc.) that every BBS instance
// expects to exist. Idempotent; safe across restarts.
func mustSeed(ctx context.Context, queries *gen.Queries, logger *slog.Logger) {
	seedCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := data.SeedDefaults(seedCtx, queries, logger); err != nil {
		logger.Error("seed defaults", "err", err)
		os.Exit(1)
	}
}

// mustBootstrapSysop creates the configured sysop account if it doesn't
// already exist. Idempotent.
func mustBootstrapSysop(ctx context.Context, pool *pgxpool.Pool, hasher *auth.Hasher, opts config.Options, logger *slog.Logger) {
	bootCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := auth.BootstrapSysop(bootCtx, pool, hasher, opts.BootstrapSysopHandle, opts.BootstrapSysopPassword, logger); err != nil {
		logger.Error("sysop bootstrap", "err", err)
		os.Exit(1)
	}
}

// mustOpenRedis parses the URL, opens the client, and pings to surface
// misconfiguration at startup rather than at first request.
func mustOpenRedis(ctx context.Context, opts config.Options, logger *slog.Logger) *redis.Client {
	redisOpts, err := redis.ParseURL(opts.RedisConnStr)
	if err != nil {
		logger.Error("redis parse url", "err", err)
		os.Exit(1)
	}
	client := redis.NewClient(redisOpts)
	if err := client.Ping(ctx).Err(); err != nil {
		logger.Error("redis ping", "err", err)
		os.Exit(1)
	}
	return client
}

// buildSessionDeps assembles every process-singleton service screens reach
// for through the Session, plus the multiplayer Hold'em registry (returned
// separately because main() needs to call Persist on it during shutdown).
//
// Side effects: starts the wall-broadcast dispatcher goroutine bound to ctx,
// restores any in-flight Hold'em tables from the DB.
func buildSessionDeps(
	ctx context.Context,
	pool *pgxpool.Pool,
	queries *gen.Queries,
	hasher *auth.Hasher,
	redisClient *redis.Client,
	opts config.Options,
	logger *slog.Logger,
) (session.Deps, *multiplayer.Registry, *roulettemp.Registry) {
	// Each service gets a logger pre-tagged with component=<name>; downstream
	// log lines inherit the tag without each call site having to repeat it.
	// slog.Logger.With is cheap (it just allocates a child handler with the
	// extra attr) and the per-service tag is the right grain for "filter
	// the log stream to one subsystem."
	bus := realtime.NewRedisBus(redisClient, logger.With("component", "redis-bus"))
	wallDispatcher := realtime.NewWallDispatcher(bus, logger.With("component", "wall"))
	go func() {
		if err := wallDispatcher.Run(ctx); err != nil {
			logger.Error("wall dispatcher exited", "err", err)
		}
	}()

	mpLedger := &realtime.MultiplayerLedger{Pool: pool, Queries: queries}
	holdemReg := multiplayer.NewRegistry(ctx, queries, mpLedger, logger.With("component", "holdem-mp"))
	restoreCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := holdemReg.Restore(restoreCtx); err != nil {
		logger.Warn("holdem: restore tables", "err", err)
	}
	cancel()

	// Roulette singleton coordinator. The registry restores the rolling
	// history from roulette_state on boot, then Coordinator.Run starts the
	// phase machine — the wheel runs forever, even with zero subscribers.
	walletSvc := &doors.WalletService{Queries: queries}
	rouletteReg := roulettemp.NewRegistry(
		ctx, queries, mpLedger,
		&roulettemp.WalletAdapter{Svc: walletSvc},
		doors.CryptoRng{},
		logger.With("component", "roulette-mp"),
	)
	restoreCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	if err := rouletteReg.Restore(restoreCtx); err != nil {
		logger.Warn("roulette: restore state", "err", err)
	}
	cancel()
	go rouletteReg.Coordinator().Run(ctx)

	deps := session.Deps{
		Core: session.CoreDeps{
			Pool:    pool,
			Queries: queries,
			Hasher:  hasher,
			Logger:  logger,
			// Process-wide inline-image scheduler shared by chat + browser.
			// Cap of 4 in-flight fetches balances responsiveness for the
			// solo user against polite throughput for image hosts.
			Images: asyncfetch.NewPool(4, 6*time.Second, 20*time.Second, logger.With("component", "image-fetch")),
		},
		Realtime: session.RealtimeDeps{
			Chat:         &realtime.ChatService{Queries: queries, Bus: bus, Logger: logger.With("component", "chat")},
			Presence:     realtime.NewPresenceService(redisClient, logger.With("component", "presence")),
			Profile:      realtime.NewProfileService(queries),
			Wall:         wallDispatcher,
			Forums:       &realtime.ForumService{Queries: queries, Logger: logger.With("component", "forums")},
			Locations:    &realtime.LocationService{Queries: queries},
			Leaderboards: &realtime.LeaderboardService{Queries: queries},
		},
		Providers: buildProviders(opts),
		Art:       buildArt(opts, logger),
		Games: session.GameDeps{
			HoldemRegistry: holdemReg,
			Roulette:       rouletteReg.Coordinator(),
			Wallet:         walletSvc,
		},
		Policy: session.PolicyDeps{
			BootstrapSysopHandle: opts.BootstrapSysopHandle,
			MinPasswordLength:    8,
		},
	}
	return deps, holdemReg, rouletteReg
}

// attachSecurity bolts the BanCache + settings.Cache onto an already-built
// sessionDeps. Kept separate from buildSessionDeps because both are
// constructed AFTER buildSessionDeps (the BanCache depends on the audit
// recorder; the settings cache depends on Redis being already pinged).
// Mutates in place.
func attachSecurity(deps *session.Deps, banCache *auth.BanCache, settingsCache *settings.Cache) {
	deps.Security = session.SecurityDeps{Bans: banCache, Settings: settingsCache}
}

// defaultsFromOptions translates the env-driven config.Options into the
// settings.Defaults the Cache layers under unset rows. Centralized here so
// the settings package stays free of a config import (which would cycle:
// settings → config → auth → settings via auth.CreateAccount).
func defaultsFromOptions(o config.Options) settings.Defaults {
	return settings.Defaults{
		SignupsEnabled:         true,
		SignupsDisabledMessage: "New account signups are temporarily paused. Please try again later.",
		MOTD:                   "",
		WallEnabled:            true,
		MaxTotalSessions:       0,
		MaxConnPerIP:           o.SSHSecurity.MaxConnPerIP,
		MaxUnauthHandshakes:    o.SSHSecurity.MaxUnauthHandshakes,
		LockoutHandleThreshold: o.RateLimit.HandleThreshold,
		LockoutIPThreshold:     o.RateLimit.IPThreshold,
		LockoutWindowSeconds:   int(o.RateLimit.WindowDuration / time.Second),
		CarbonylEnabled:        o.Carbonyl.Enabled,
		CarbonylMaxGlobal:      o.Carbonyl.MaxGlobal,
		CarbonylMaxPerIP:       o.Carbonyl.MaxPerIP,
		CarbonylMaxPerHandle:   o.Carbonyl.MaxPerHandle,
	}
}

// buildProviders constructs the outbound HTTP-cached integrations. One Cache
// per finance asset class so a Yahoo outage doesn't poison CoinGecko results
// (and vice versa). All providers share one *http.Transport so the
// per-host TCP+TLS connection pool is amortized across CoinGecko, Yahoo,
// Frankfurter, NWS, OpenMeteo, and HackerNews — each previously stood up
// its own Transport and paid handshake cost on every fetch.
func buildProviders(opts config.Options) session.ProviderDeps {
	sharedTransport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	// newClient returns a *http.Client that re-uses sharedTransport but
	// carries a per-provider hard ceiling. Per-call context.WithTimeout
	// in the screens still applies and is usually shorter than this; the
	// client Timeout is a backstop for callers that forgot to pass one.
	newClient := func(timeout time.Duration) *http.Client {
		return &http.Client{Transport: sharedTransport, Timeout: timeout}
	}

	yahoo := finance.NewYahoo()
	yahoo.HTTPClient = newClient(10 * time.Second)
	coingecko := finance.NewCoinGecko()
	coingecko.HTTPClient = newClient(8 * time.Second)
	frankfurter := finance.NewFrankfurter()
	frankfurter.HTTPClient = newClient(10 * time.Second)
	financeNews := finance.NewYahooRSSNews()
	financeNews.HTTPClient = newClient(10 * time.Second)

	hn := news.NewHackerNews()
	hn.HTTPClient = newClient(10 * time.Second)

	lob := news.NewLobsters()
	lob.HTTPClient = newClient(10 * time.Second)

	om := weather.NewOpenMeteo()
	om.HTTPClient = newClient(10 * time.Second)
	nws := weather.NewNWSAlerts("")
	nws.HTTP = newClient(10 * time.Second)

	tiles := maptile.New("")
	tiles.HTTP = newClient(8 * time.Second)

	geo := geocoding.NewOpenMeteo()
	geo.HTTPClient = newClient(5 * time.Second)

	// Routing is opt-in via NIGHTMS_ORS_API_KEY. An empty key leaves the
	// provider nil; the map screen handles nil by toasting "routing disabled".
	var routingProv routing.Provider
	if opts.ORSAPIKey != "" {
		ors := routing.NewOpenRouteService(opts.ORSAPIKey)
		ors.HTTPClient = newClient(10 * time.Second)
		routingProv = ors
	}

	// Per-source TTLs: HN's front page churns within a few minutes so a 5m
	// window keeps cached views feeling fresh without hammering Firebase.
	// Lobsters moves much slower — its front page is roughly hourly — so a
	// longer TTL is appropriate and friendlier to a small self-hosted site.
	newsRegistry := news.NewRegistry(
		news.Source{
			ID:          news.SourceIDHackerNews,
			DisplayName: "HN",
			Host:        "news.ycombinator.com",
			Hotkey:      '1',
			Provider:    news.NewCache(hn, 5*time.Minute),
		},
		news.Source{
			ID:          news.SourceIDLobsters,
			DisplayName: "Lobsters",
			Host:        "lobste.rs",
			Hotkey:      '2',
			Provider:    news.NewCache(lob, 15*time.Minute),
		},
	)

	return session.ProviderDeps{
		News:    newsRegistry,
		Weather: om,
		Alerts:  weather.NewNWSCache(nws, 5*time.Minute),
		Finance: &finance.Multi{
			Stock:  finance.NewCache(yahoo, 60*time.Second),
			Crypto: finance.NewCache(coingecko, 60*time.Second),
			Fx:     finance.NewCache(frankfurter, 5*time.Minute),
		},
		FinanceNews: financeNews,
		MapTiles:    tiles,
		Geocoder:    geo,
		Routing:     routingProv,
	}
}

// buildArt constructs the filesystem-backed visual asset providers from
// operator-supplied directories. All three icon providers tolerate missing
// directories — screens fall back to embedded defaults.
func buildArt(opts config.Options, logger *slog.Logger) session.ArtDeps {
	artLogger := logger.With("component", "art")
	return session.ArtDeps{
		LoginBanner:     art.NewLoginBannerProvider(opts.LoginArtPath),
		LobbyIcons:      art.NewFileSystemLobbyIcons(opts.LobbyIconsDir, artLogger),
		BoardIcons:      art.NewFileSystemBoardIcons(opts.BoardIconsDir, artLogger),
		GalleryProvider: &art.FileSystemGallery{Dir: opts.ArtDir},
	}
}

// runListeners launches the SSH + HTTP servers and blocks until either
// returns an error or ctx is cancelled by SIGINT/SIGTERM. On shutdown it
// persists any in-flight Hold'em tables before draining the listeners.
// sshListener is the netlimit-wrapped TCP listener already bound to the SSH
// address; transport serves on it via ServeWithListener so the per-IP gates
// run before the SSH handshake.
func runListeners(ctx context.Context, srv *transport.Server, sshListener net.Listener, webSrv *web.Server, holdemReg *multiplayer.Registry, rouletteReg *roulettemp.Registry, logger *slog.Logger) {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ServeWithListener(sshListener) }()
	go func() {
		if err := webSrv.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := holdemReg.Persist(shutCtx); err != nil {
			logger.Warn("holdem: persist on shutdown", "err", err)
		}
		if err := rouletteReg.Persist(shutCtx); err != nil {
			logger.Warn("roulette: persist on shutdown", "err", err)
		}
		_ = srv.Shutdown(shutCtx)
		_ = webSrv.Shutdown(shutCtx)
	case err := <-errCh:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			logger.Error("server exited with error", "err", err)
			os.Exit(1)
		}
	}
}

// buildOAuthProviders constructs the two OAuth provider configs from the
// options struct. Either may be nil if its CLIENT_ID is empty. The redirect
// base falls back to "http://<WebPublicHost>:<port>" for dev convenience;
// production deployments behind a proxy should set NIGHTMS_OAUTH_REDIRECT_BASE
// to the externally-visible https origin.
func buildOAuthProviders(opts config.Options) web.OAuthProviders {
	base := opts.OAuthRedirectBase
	if base == "" {
		scheme := "http"
		if opts.WebSecureCookies {
			scheme = "https"
		}
		port := portFromHTTPAddr(opts.HTTPAddr)
		if port != "" && port != "80" && port != "443" {
			base = fmt.Sprintf("%s://%s:%s", scheme, opts.WebPublicHost, port)
		} else {
			base = fmt.Sprintf("%s://%s", scheme, opts.WebPublicHost)
		}
	}
	var out web.OAuthProviders
	if opts.GoogleClientID != "" {
		out.Google = auth.NewGoogleProvider(opts.GoogleClientID, opts.GoogleClientSecret, base+"/auth/google/callback")
	}
	if opts.MicrosoftClientID != "" {
		out.Microsoft = auth.NewMicrosoftProvider(opts.MicrosoftClientID, opts.MicrosoftClientSecret, base+"/auth/microsoft/callback")
	}
	return out
}

func portFromHTTPAddr(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
}
