// Package config carries the typed env-var binding for the server. Go has no
// DI container, so it's just a struct constructed in main.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nickna/ssh.night.ms/internal/auth"
)

type Options struct {
	SSHAddr     string
	HTTPAddr    string
	HostKeyDir  string
	IdleTimeout time.Duration

	// LogLevel selects the slog handler's verbosity. Read from
	// NIGHTMS_LOG_LEVEL (debug|info|warn|error); defaults to info.
	LogLevel slog.Level

	// DebugAddr enables a localhost pprof + /debug/vars listener when
	// non-empty. Sourced from NIGHTMS_DEBUG_ADDR (e.g. "127.0.0.1:6060").
	// Bind to a local interface only — never expose to the public network.
	DebugAddr string

	DBConnStr    string
	RedisConnStr string

	// DBMaxConns caps the pgxpool size. The pgx default is max(4, NumCPU)
	// which is far too low for a process serving SSH + HTTP + realtime
	// fan-out concurrently; one finger-lookup screen can chew several
	// connections before releasing them. NIGHTMS_DB_MAX_CONNS overrides.
	DBMaxConns int32

	BootstrapSysopHandle   string
	BootstrapSysopPassword string

	WebPublicHost    string
	WebCookieSecret  []byte // hex-decoded; pass-through to web.Config
	WebSecureCookies bool
	PFPDir           string // profile picture storage directory
	ArtDir           string // .ans gallery directory
	LobbyIconsDir    string // .ans glyphs shown inside lobby carousel cards
	BoardIconsDir    string // .ans glyphs shown next to each forum row
	LoginArtPath     string // optional path to a .ans login banner

	Argon2Params auth.Argon2Params
	RateLimit    auth.RateLimitParams

	// PersistentBanDuration is the TTL applied to security_ip_bans rows
	// written by the auto-promotion path (when an IP's lockcount crosses
	// RateLimit.PersistentBanThreshold). Sysop-issued bans take an explicit
	// duration via the ban-ip command. Default 24h.
	PersistentBanDuration time.Duration

	// SSHSecurity bundles the connection-level controls applied to the SSH
	// listener: protocol-layer caps (MaxAuthTries, LoginGrace) and the
	// netlimit gates (per-IP concurrent + token bucket, global
	// unauthenticated-handshake cap). See the Security section of CLAUDE.md
	// for the full env-var contract.
	SSHSecurity SSHSecurityOptions

	// OAuth — each pair is independent. Empty client_id disables the
	// provider; the login page hides its button accordingly. OAuthRedirectBase
	// is the externally-reachable scheme + host (+ port) the provider
	// redirects back to; defaults to http://<WebPublicHost> for dev.
	GoogleClientID        string
	GoogleClientSecret    string
	MicrosoftClientID     string
	MicrosoftClientSecret string
	OAuthRedirectBase     string

	// Device-flow client for Google. Google's device endpoint rejects
	// regular Web-application OAuth clients (returns disabled_client) — a
	// separate "TVs and Limited Input devices" client must be registered in
	// Google Cloud Console and its credentials supplied here. When either is
	// empty, the TUI add-account flow surfaces "linking from terminal is
	// unavailable" rather than erroring; the web /profile/connections page
	// still works via the auth-code redirect.
	GoogleDeviceClientID     string
	GoogleDeviceClientSecret string

	// OAuth refresh + encryption knobs. OAuthTokenSecret overrides the
	// default (HKDF of WebCookieSecret) so token encryption can be rotated
	// independent of session cookies. Refresher cadence + look-ahead govern
	// the background "renew before expiry" loop. BatchSize + Workers tune
	// per-tick throughput — defaults handle O(100) linked accounts; bump
	// both if a sign-up wave clusters too many expiries into one window.
	OAuthRefreshInterval  time.Duration
	OAuthRefreshLeadTime  time.Duration
	OAuthRefreshBatchSize int
	OAuthRefreshWorkers   int
	OAuthTokenSecret      []byte

	// ORSAPIKey enables the Map screen's directions affordance when set.
	// Empty disables routing — the map still works for browsing.
	ORSAPIKey string

	// Carbonyl bundles the rich-mode browser config. BinPath / DataDir take
	// effect at boot; the Enabled flag + concurrency caps seed the matching
	// settings.Defaults so a sysop can flip them live without restart.
	Carbonyl CarbonylOptions
}

// CarbonylOptions configures the "rich mode" handoff in the browser screen.
// All NIGHTMS_CARBONYL_* env vars; safe to leave defaulted (feature ships
// disabled until the sysop flips carbonyl_enabled=true via the settings tab).
type CarbonylOptions struct {
	// BinPath is the absolute path to the extracted carbonyl binary. Default
	// /opt/carbonyl/carbonyl matches the Dockerfile bundle layout. When the
	// path doesn't exist, the BBS still boots — carbonyl.New returns
	// ErrBinaryMissing and rich mode is left disabled at the screen layer.
	BinPath string

	// DataDir is the parent for per-user --user-data-dir subdirs (one per
	// userid, mode 0700, lazy-created). Default /data/carbonyl; lives on the
	// same persistent volume as host-keys / pfp / gallery.
	DataDir string

	// Enabled seeds settings.Defaults.CarbonylEnabled. False by default so the
	// feature ships dark; flip via sysop UI after smoke-testing in prod.
	Enabled bool

	// MaxGlobal / MaxPerIP / MaxPerHandle seed the matching settings.Defaults
	// concurrency caps. Tuned conservatively because each carbonyl child is
	// hundreds of MB resident — runaway launches OOM the container.
	MaxGlobal    int
	MaxPerIP     int
	MaxPerHandle int
}

// SSHSecurityOptions holds the SSH listener's protocol- and network-layer
// hardening knobs. Defaults are picked to be safe out of the box for a
// small public BBS; production deployments can tighten them via the
// NIGHTMS_SSH_* env vars.
type SSHSecurityOptions struct {
	// MaxAuthTries → gossh.ServerConfig.MaxAuthTries. Default 3.
	MaxAuthTries int

	// LoginGrace caps the wall-clock time the handshake + auth has to
	// complete; zero disables. Default 30s.
	LoginGrace time.Duration

	// MaxConnPerIP caps concurrent TCP conns from one source (IPv6 /64-
	// collapsed). Zero disables. Default 10.
	MaxConnPerIP int

	// PerIPConnRate is the new-conn token-bucket rate (conns/sec) per source.
	// Zero disables the rate-limit. Default 5.
	PerIPConnRate float64

	// PerIPConnBurst is the bucket depth. Zero disables. Default 20.
	PerIPConnBurst int

	// MaxUnauthHandshakes caps in-flight unauthenticated handshakes process-
	// wide (the in-process MaxStartups). Zero disables. Default 100.
	MaxUnauthHandshakes int
}

// Load reads environment variables and falls back to sensible defaults that
// match the legacy stack's defaults (so the same .env file works for both).
func Load() Options {
	o := Options{
		SSHAddr:     net.JoinHostPort("0.0.0.0", envOr("BBS_SSH_PORT", "2222")),
		HTTPAddr:    net.JoinHostPort("0.0.0.0", envOr("BBS_HTTP_PORT", "5080")),
		HostKeyDir:  envOr("NIGHTMS_HOST_KEY_DIR", filepath.Join("data", "host-keys")),
		IdleTimeout: 30 * time.Minute,
		LogLevel:    parseLogLevel(os.Getenv("NIGHTMS_LOG_LEVEL")),
		DebugAddr:   os.Getenv("NIGHTMS_DEBUG_ADDR"),

		// URL form (not libpq key=value): golang-migrate parses the scheme from
		// the prefix; pgx accepts both forms equally, so the URL works for the pool too.
		DBConnStr:    envOr("NIGHTMS_DB_CONN", "postgres://postgres:postgres@127.0.0.1:55432/bbs?sslmode=disable"),
		RedisConnStr: envOr("NIGHTMS_REDIS_CONN", "redis://127.0.0.1:56379"),
		DBMaxConns:   int32(uintEnv("NIGHTMS_DB_MAX_CONNS", 16)),

		BootstrapSysopHandle:   os.Getenv("NIGHTMS_BOOTSTRAP_SYSOP_HANDLE"),
		BootstrapSysopPassword: os.Getenv("NIGHTMS_BOOTSTRAP_SYSOP_PASSWORD"),

		Argon2Params: auth.Argon2Params{
			MemoryKB:    uintEnv("NIGHTMS_ARGON2_MEM_KB", 65536),
			Iterations:  uintEnv("NIGHTMS_ARGON2_ITERATIONS", 3),
			Parallelism: uint8(uintEnv("NIGHTMS_ARGON2_PARALLELISM", 1)),
			SaltBytes:   uintEnv("NIGHTMS_ARGON2_SALT_BYTES", 16),
			HashBytes:   uintEnv("NIGHTMS_ARGON2_HASH_BYTES", 32),
		},
		WebPublicHost:    loadWebPublicHost(os.Getenv("NIGHTMS_WEB_BASE_URL")),
		WebSecureCookies: os.Getenv("NIGHTMS_WEB_SECURE_COOKIES") == "1",
		PFPDir:           envOr("NIGHTMS_PFP_DIR", filepath.Join("data", "profile-pictures")),
		ArtDir:           envOr("NIGHTMS_ART_DIR", filepath.Join("data", "art", "gallery")),
		LobbyIconsDir:    envOr("NIGHTMS_LOBBY_ICONS_DIR", filepath.Join("data", "art", "lobby-icons")),
		BoardIconsDir:    envOr("NIGHTMS_BOARD_ICONS_DIR", filepath.Join("data", "art", "board-icons")),
		LoginArtPath:     os.Getenv("NIGHTMS_LOGIN_ART_PATH"),
		RateLimit: auth.RateLimitParams{
			HandleThreshold:        int(uintEnv("NIGHTMS_LOCKOUT_HANDLE_THRESHOLD", 5)),
			IPThreshold:            int(uintEnv("NIGHTMS_LOCKOUT_IP_THRESHOLD", 20)),
			WindowDuration:         time.Duration(uintEnv("NIGHTMS_LOCKOUT_WINDOW_SECONDS", 900)) * time.Second,
			LockDuration:           time.Duration(uintEnv("NIGHTMS_LOCKOUT_SECONDS", 900)) * time.Second,
			BackoffMax:             int(uintEnv("NIGHTMS_LOCKOUT_BACKOFF_MAX", 5)),
			PersistentBanThreshold: int(uintEnv("NIGHTMS_PERSISTENT_BAN_THRESHOLD", 3)),
			LockcountWindow:        time.Duration(uintEnv("NIGHTMS_LOCKCOUNT_WINDOW_SECONDS", 86400)) * time.Second,
		},
		PersistentBanDuration: time.Duration(uintEnv("NIGHTMS_PERSISTENT_BAN_DURATION_SECONDS", 86400)) * time.Second,
		SSHSecurity: SSHSecurityOptions{
			MaxAuthTries:        int(uintEnv("NIGHTMS_SSH_MAX_AUTH_TRIES", 3)),
			LoginGrace:          time.Duration(uintEnv("NIGHTMS_SSH_LOGIN_GRACE_SECONDS", 30)) * time.Second,
			MaxConnPerIP:        int(uintEnv("NIGHTMS_SSH_MAX_CONN_PER_IP", 10)),
			PerIPConnRate:       floatEnv("NIGHTMS_SSH_CONN_RATE_PER_IP", 5),
			PerIPConnBurst:      int(uintEnv("NIGHTMS_SSH_CONN_BURST_PER_IP", 20)),
			MaxUnauthHandshakes: int(uintEnv("NIGHTMS_SSH_MAX_UNAUTH_HANDSHAKES", 100)),
		},
		GoogleClientID:           os.Getenv("NIGHTMS_GOOGLE_CLIENT_ID"),
		GoogleClientSecret:       os.Getenv("NIGHTMS_GOOGLE_CLIENT_SECRET"),
		MicrosoftClientID:        os.Getenv("NIGHTMS_MICROSOFT_CLIENT_ID"),
		MicrosoftClientSecret:    os.Getenv("NIGHTMS_MICROSOFT_CLIENT_SECRET"),
		OAuthRedirectBase:        os.Getenv("NIGHTMS_OAUTH_REDIRECT_BASE"),
		GoogleDeviceClientID:     os.Getenv("NIGHTMS_GOOGLE_DEVICE_CLIENT_ID"),
		GoogleDeviceClientSecret: os.Getenv("NIGHTMS_GOOGLE_DEVICE_CLIENT_SECRET"),
		OAuthRefreshInterval:     durationEnv("NIGHTMS_OAUTH_REFRESH_INTERVAL", 60*time.Second),
		OAuthRefreshLeadTime:     durationEnv("NIGHTMS_OAUTH_REFRESH_LEAD_TIME", 10*time.Minute),
		OAuthRefreshBatchSize:    int(uintEnv("NIGHTMS_OAUTH_REFRESH_BATCH_SIZE", 50)),
		OAuthRefreshWorkers:      int(uintEnv("NIGHTMS_OAUTH_REFRESH_WORKERS", 4)),
		OAuthTokenSecret:         hexBytesEnv("NIGHTMS_OAUTH_TOKEN_SECRET"),
		ORSAPIKey:             os.Getenv("NIGHTMS_ORS_API_KEY"),
		Carbonyl: CarbonylOptions{
			BinPath: envOr("NIGHTMS_CARBONYL_BIN_PATH", "/opt/carbonyl/carbonyl"),
			DataDir: envOr("NIGHTMS_CARBONYL_DATA_DIR", filepath.Join("data", "carbonyl")),
			// Default ON. Carbonyl is only actually offered to a session when
			// the binary is also present (carbonyl.New stats the path at
			// boot); shipping the kill switch off-by-default made the feature
			// undiscoverable. Set NIGHTMS_CARBONYL_ENABLED=0 to disable.
			Enabled:      envOr("NIGHTMS_CARBONYL_ENABLED", "1") != "0",
			MaxGlobal:    int(uintEnv("NIGHTMS_CARBONYL_MAX_GLOBAL", 2)),
			MaxPerIP:     int(uintEnv("NIGHTMS_CARBONYL_MAX_PER_IP", 1)),
			MaxPerHandle: int(uintEnv("NIGHTMS_CARBONYL_MAX_PER_HANDLE", 1)),
		},
	}
	o.WebCookieSecret = loadCookieSecret(o.HostKeyDir)
	return o
}

// parseLogLevel maps NIGHTMS_LOG_LEVEL strings to slog.Level. Unknown or
// empty values default to Info — production-safe; debug must be opted into.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// loadWebPublicHost parses NIGHTMS_WEB_BASE_URL into a bare host. Accepts
// either shape — "night.ms" or "https://night.ms" — so the value the legacy
// stack carried in this env var keeps working after cutover. Returns the
// bare host (with port when one is present); callers prefix the scheme.
func loadWebPublicHost(s string) string {
	if s == "" {
		return "localhost"
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			panic(fmt.Sprintf("config: NIGHTMS_WEB_BASE_URL must parse: %v", err))
		}
		if u.Host == "" {
			panic(fmt.Sprintf("config: NIGHTMS_WEB_BASE_URL has no host: %q", s))
		}
		return u.Host
	}
	return s
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadCookieSecret returns the web cookie-signing key. Order of preference:
//  1. NIGHTMS_COOKIE_SECRET (hex, ≥ 32 bytes) — explicit operator override.
//  2. dir/cookie-secret on disk — read if present, generated + persisted
//     (mode 0600) otherwise. Stable across restarts because dir is the
//     persistent /data/host-keys volume in prod.
//  3. Ephemeral random key — only reached when dir is empty (e.g. unit
//     tests). Cookies invalidate on restart.
//
// In case 2, a disk write failure panics rather than silently falling back
// to ephemeral — a misconfigured volume in prod should fail loud.
func loadCookieSecret(dir string) []byte {
	if hexStr := os.Getenv("NIGHTMS_COOKIE_SECRET"); hexStr != "" {
		b, err := hex.DecodeString(hexStr)
		if err != nil {
			panic(fmt.Sprintf("config: NIGHTMS_COOKIE_SECRET must be hex: %v", err))
		}
		if len(b) < 32 {
			panic("config: NIGHTMS_COOKIE_SECRET must decode to at least 32 bytes")
		}
		return b
	}

	if dir != "" {
		path := filepath.Join(dir, "cookie-secret")
		if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
			return b
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic(fmt.Sprintf("config: random cookie secret: %v", err))
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			panic(fmt.Sprintf("config: mkdir %s for cookie secret: %v", dir, err))
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			panic(fmt.Sprintf("config: write %s: %v", path, err))
		}
		return b
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("config: random cookie secret: %v", err))
	}
	return b
}

func floatEnv(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		panic(fmt.Sprintf("config: %s must be a float, got %q: %v", key, v, err))
	}
	return n
}

func uintEnv(key string, fallback uint32) uint32 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		panic(fmt.Sprintf("config: %s must be a uint, got %q: %v", key, v, err))
	}
	return uint32(n)
}

// durationEnv parses a Go duration string ("60s", "5m", "1h30m"). Panics on
// malformed input — these knobs are operator-set, not user-set.
func durationEnv(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		panic(fmt.Sprintf("config: %s must be a duration like '60s' / '10m', got %q: %v", key, v, err))
	}
	return d
}

// hexBytesEnv decodes a hex-encoded secret env var into bytes. Returns
// nil when unset (callers fall back to a default key source). Panics on
// malformed hex so a typo is caught at boot, not at first use.
func hexBytesEnv(key string) []byte {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		panic(fmt.Sprintf("config: %s must be hex, got %q: %v", key, v, err))
	}
	return b
}
