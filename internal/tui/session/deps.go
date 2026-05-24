package session

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/holdem/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/imaging/asyncfetch"
	"github.com/nickna/ssh.night.ms/internal/providers/bookmarks"
	"github.com/nickna/ssh.night.ms/internal/providers/finance"
	"github.com/nickna/ssh.night.ms/internal/providers/geocoding"
	"github.com/nickna/ssh.night.ms/internal/providers/maptile"
	"github.com/nickna/ssh.night.ms/internal/providers/news"
	"github.com/nickna/ssh.night.ms/internal/providers/routing"
	"github.com/nickna/ssh.night.ms/internal/providers/search"
	"github.com/nickna/ssh.night.ms/internal/providers/weather"
	"github.com/nickna/ssh.night.ms/internal/realtime"
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
}

// CoreDeps is the irreducible base every layer needs.
type CoreDeps struct {
	Pool    *pgxpool.Pool
	Queries *gen.Queries
	Hasher  *auth.Hasher
	Logger  *slog.Logger

	// Images is the process-wide inline-image fetch scheduler. Screens that
	// paint URLs (chat, browser) share it so a paste-storm in one can't
	// starve the other's network budget.
	Images *asyncfetch.Pool
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
}

// ProviderDeps groups the outbound HTTP-cached integrations.
type ProviderDeps struct {
	News        news.Provider
	Weather     weather.Provider
	Alerts      weather.AlertProvider
	Finance     finance.Provider
	FinanceNews finance.NewsProvider
	MapTiles    *maptile.Provider
	Search      search.Provider
	Bookmarks   *bookmarks.Service
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

	// Wallet is the process-wide WalletService each door game uses to load,
	// debit, and credit credits and to record ledger entries. Stateless
	// wrapper over Queries — one instance for all sessions.
	Wallet *doors.WalletService
}

// PolicyDeps groups env-derived policy knobs needed at session-attach or by
// the register flow. WeatherDefaults flows through to every Session via
// WeatherCoords(); the other two are only read by transport during signup.
type PolicyDeps struct {
	WeatherDefaults      WeatherDefaults
	BootstrapSysopHandle string
	MinPasswordLength    int
}

// SecurityDeps surfaces the BanCache to the sysop screen so the security
// tab can list active bans and process ban-ip / unban-ip commands without
// going through a Postgres query on every render. Nil-safe — screens
// check before dereferencing so a stripped-down test harness can omit it.
type SecurityDeps struct {
	Bans *auth.BanCache
}
