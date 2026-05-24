// Package config carries the typed env-var binding for the server. Mirrors
// NightMsOptions / PasswordHashingOptions from the .NET project, but inlined —
// Go has no DI container, so it's just a struct constructed in main.
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

	WeatherLat   float64
	WeatherLon   float64
	WeatherLabel string

	Argon2Params auth.Argon2Params
	RateLimit    auth.RateLimitParams

	// OAuth — each pair is independent. Empty client_id disables the
	// provider; the login page hides its button accordingly. OAuthRedirectBase
	// is the externally-reachable scheme + host (+ port) the provider
	// redirects back to; defaults to http://<WebPublicHost> for dev.
	GoogleClientID        string
	GoogleClientSecret    string
	MicrosoftClientID     string
	MicrosoftClientSecret string
	OAuthRedirectBase     string

	// ORSAPIKey enables the Map screen's directions affordance when set.
	// Empty disables routing — the map still works for browsing.
	ORSAPIKey string
}

// Load reads environment variables and falls back to sensible defaults that
// match the .NET stack's defaults (so the same .env file works for both).
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
		WeatherLat:       floatEnv("NIGHTMS_WEATHER_LAT", 40.7128),
		WeatherLon:       floatEnv("NIGHTMS_WEATHER_LON", -74.0060),
		WeatherLabel:     envOr("NIGHTMS_WEATHER_LABEL", "New York"),
		RateLimit: auth.RateLimitParams{
			HandleThreshold: int(uintEnv("NIGHTMS_LOCKOUT_HANDLE_THRESHOLD", 5)),
			IPThreshold:     int(uintEnv("NIGHTMS_LOCKOUT_IP_THRESHOLD", 20)),
			WindowDuration:  time.Duration(uintEnv("NIGHTMS_LOCKOUT_WINDOW_SECONDS", 900)) * time.Second,
			LockDuration:    time.Duration(uintEnv("NIGHTMS_LOCKOUT_SECONDS", 900)) * time.Second,
		},
		GoogleClientID:        os.Getenv("NIGHTMS_GOOGLE_CLIENT_ID"),
		GoogleClientSecret:    os.Getenv("NIGHTMS_GOOGLE_CLIENT_SECRET"),
		MicrosoftClientID:     os.Getenv("NIGHTMS_MICROSOFT_CLIENT_ID"),
		MicrosoftClientSecret: os.Getenv("NIGHTMS_MICROSOFT_CLIENT_SECRET"),
		OAuthRedirectBase:     os.Getenv("NIGHTMS_OAUTH_REDIRECT_BASE"),
		ORSAPIKey:             os.Getenv("NIGHTMS_ORS_API_KEY"),
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
// either shape — "night.ms" or "https://night.ms" — so the value the .NET
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
