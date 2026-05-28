package session

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/devicecode"
	"github.com/nickna/ssh.night.ms/internal/auth/tokenseal"
	"github.com/nickna/ssh.night.ms/internal/carbonyl"
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
	"github.com/nickna/ssh.night.ms/internal/settings"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
)

// Deps is the process-singleton bag passed to both transport (SSH) and web
// (WebSocket) when they spin up a per-user session. Grouping deps by concern
// keeps main.go composable and screens browseable — each screen needs at most
// one or two of the inner groups.
//
// Both transport and web build a Session by handing this same Deps value to
// session.New(); that's the single source of truth for what a screen sees.
type Deps struct {
	Core      CoreDeps
	Realtime  RealtimeDeps
	Providers ProviderDeps
	Art       ArtDeps
	Games     GameDeps
	Policy    PolicyDeps
	Security  SecurityDeps
	System    SystemDeps
}

// CoreDeps is the irreducible base every layer needs.
type CoreDeps struct {
	Pool    *pgxpool.Pool
	Queries *gen.Queries
	Hasher  *auth.Hasher
	Logger  *slog.Logger

	// Images is the process-wide inline-image fetch scheduler shared by every
	// screen that paints URLs (chat etc.) so a paste-storm in one can't
	// starve the others' network budget.
	Images *asyncfetch.Pool

	// OAuthSealer encrypts/decrypts OAuth tokens at rest. Nil when neither
	// NIGHTMS_OAUTH_TOKEN_SECRET nor a cookie secret is available — screens
	// gate on nil for the rare misconfigured case.
	OAuthSealer *tokenseal.Sealer

	// OAuthDeviceCode drives the TUI account-linking flow. Nil when no
	// OAuth client is configured at all; the profile screen surfaces a
	// "linking is unavailable" message.
	OAuthDeviceCode *devicecode.Service
}

// RealtimeDeps groups the Postgres + Redis-backed live services.
type RealtimeDeps struct {
	Chat         *realtime.ChatService
	Presence     *realtime.PresenceService
	Profile      *realtime.ProfileService
	Wall         *realtime.WallDispatcher
	Forums       *realtime.ForumService
	Locations    *realtime.LocationService
	Leaderboards *realtime.LeaderboardService

	// Kicker drives the sysop "kick / delete-user" connection-close fanout.
	// Sessions register a Close func at construction time; SessionKicker.Kick
	// invokes every matching registration locally and publishes to Redis so
	// peer replicas do the same.
	Kicker *realtime.SessionKicker
}

// ProviderDeps groups the outbound HTTP-cached integrations.
type ProviderDeps struct {
	// News is the registry of news sources the BBS exposes. The News screen
	// renders one tab per registered source. Each Source carries its own
	// Provider (typically already wrapped in a per-source TTL cache so one
	// upstream outage can't poison the others).
	News        *news.Registry
	Weather     weather.Provider
	Alerts      weather.AlertProvider
	Finance     finance.Provider
	FinanceNews finance.NewsProvider
	MapTiles    *maptile.Provider
	Geocoder    geocoding.Provider

	// Routing is nil when NIGHTMS_ORS_API_KEY isn't set. The Map screen
	// handles nil by surfacing a "routing disabled" toast on the `d` key.
	Routing routing.Provider
}

// ArtDeps groups the filesystem-backed visual asset providers.
type ArtDeps struct {
	LoginBanner     *art.FileLoginBannerProvider
	LobbyIcons      art.LobbyIconProvider
	BoardIcons      art.LobbyIconProvider
	GalleryProvider GalleryProvider
}

// GameDeps groups stateful game registries and shared services used by the
// door screens.
type GameDeps struct {
	HoldemRegistry *multiplayer.Registry

	// Roulette is the singleton multiplayer roulette coordinator. Lifetime
	// is owned by main.go via roulettemp.Registry; screens reach the live
	// coordinator through this pointer. Nil during early boot or when the
	// registry is configured-out (tests).
	Roulette *roulettemp.Coordinator

	// Wallet is the process-wide WalletService each door game uses to load,
	// debit, and credit credits and to record ledger entries. Stateless
	// wrapper over Queries — one instance for all sessions.
	Wallet *doors.WalletService
}

// PolicyDeps groups env-derived policy knobs needed at session-attach or by
// the register flow. Read by transport during signup.
type PolicyDeps struct {
	BootstrapSysopHandle string
	MinPasswordLength    int
}

// SecurityDeps surfaces the BanCache + the runtime-settings Cache to screens.
// The sysop screen consumes both — Bans for the IP-ban tab, Settings for the
// new Settings tab and for the wall-broadcast gate. Other screens read
// Settings for MOTD (lobby) and the signup gate (register). Nil-safe —
// screens check before dereferencing so a stripped-down test harness can
// omit either.
type SecurityDeps struct {
	Bans     *auth.BanCache
	Settings *settings.Cache
}

// SystemDeps groups process-singletons that drive an out-of-process feature
// from inside a screen. Currently just the Carbonyl runner — the "rich mode"
// hand-off for the browser screen. Nil when the binary isn't on disk; screens
// gate on `m.sess.Carbonyl == nil` and surface a toast.
type SystemDeps struct {
	Carbonyl *carbonyl.Runner
}
