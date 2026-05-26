// Package settings holds the sysop-tunable runtime settings cache. The shape
// mirrors auth.BanCache: a KV table in Postgres, an in-process snapshot
// refreshed every 30s, and Redis pub/sub push-invalidation so multi-replica
// deployments converge in <1s.
//
// Defaults flow in from config.Options at construction; unset rows in the
// table return those defaults. This means new settings ship without a backfill
// migration and existing operators keep getting their env-var values.
//
// Read-path: cache.Get() returns a Snapshot by atomic.Pointer load + struct
// copy. No locks on the hot path — auth, netlimit, and screens call Get() on
// every check.
//
// Write-path: Set / Reset upsert/delete the row, force a refresh, then publish
// on "system:settings-invalidate". Other replicas re-read on receipt.
package settings

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// invalidateChannel is the Redis pub/sub channel for cross-replica fanout.
// The payload is the affected key (or "*" for unspecified) but the consumer
// always re-reads the full table on receipt — selective invalidation is not
// worth the complexity for ~10 settings.
const invalidateChannel = "system:settings-invalidate"

// Setting keys. Anything stored in system_settings.key must match one of these
// constants; the Cache logs a warning + ignores unknown keys (graceful
// downgrade).
const (
	KeySignupsEnabled         = "signups_enabled"
	KeySignupsDisabledMessage = "signups_disabled_message"
	KeyMOTD                   = "motd"
	KeyWallEnabled            = "wall_enabled"
	KeyMaxTotalSessions       = "max_total_sessions"
	KeyMaxConnPerIP           = "max_conn_per_ip"
	KeyMaxUnauthHandshakes    = "max_unauth_handshakes"
	KeyLockoutHandleThreshold = "lockout_handle_threshold"
	KeyLockoutIPThreshold     = "lockout_ip_threshold"
	KeyLockoutWindowSeconds   = "lockout_window_seconds"

	// Rich-mode (Carbonyl) keys. CarbonylEnabled gates the R hotkey on the
	// browser screen; the three caps bound concurrent rich-mode launches.
	// Defaults ship the feature dark (CarbonylEnabled=false) so prod can
	// smoke-test before announcing.
	KeyCarbonylEnabled      = "carbonyl_enabled"
	KeyCarbonylMaxGlobal    = "carbonyl_max_global"
	KeyCarbonylMaxPerIP     = "carbonyl_max_per_ip"
	KeyCarbonylMaxPerHandle = "carbonyl_max_per_handle"
)

// Value-type tags stored in system_settings.type. Enforced by the table's
// CHECK constraint and validated again here on Set so a malformed value is
// rejected before it lands in the DB.
const (
	TypeBool   = "bool"
	TypeInt    = "int"
	TypeString = "string"
)

// SettingDef describes a known setting for the sysop UI catalog. Type drives
// validation in Set; Description is rendered on the Settings tab next to the
// current value.
type SettingDef struct {
	Key         string
	Type        string
	Description string
}

// Catalog is the closed list of recognized settings. The sysop UI iterates
// this for display; Set validates against it.
var Catalog = []SettingDef{
	{KeySignupsEnabled, TypeBool, "Allow new user signups (SSH + OAuth)"},
	{KeySignupsDisabledMessage, TypeString, "Message shown when signups are disabled"},
	{KeyMOTD, TypeString, "Message of the day (rendered on lobby; empty hides)"},
	{KeyWallEnabled, TypeBool, "Allow the wall broadcast command"},
	{KeyMaxTotalSessions, TypeInt, "Cap on concurrent authenticated sessions (0 = unlimited)"},
	{KeyMaxConnPerIP, TypeInt, "Max concurrent TCP conns per source IP (0 = unlimited)"},
	{KeyMaxUnauthHandshakes, TypeInt, "Global cap on in-flight unauth SSH handshakes (0 = unlimited)"},
	{KeyLockoutHandleThreshold, TypeInt, "Auth failures per-handle to trip a lockout"},
	{KeyLockoutIPThreshold, TypeInt, "Auth failures per-IP to trip a lockout"},
	{KeyLockoutWindowSeconds, TypeInt, "Sliding window seconds for lockout counters"},
	{KeyCarbonylEnabled, TypeBool, "Allow 'rich mode' (Carbonyl browser) in SSH sessions"},
	{KeyCarbonylMaxGlobal, TypeInt, "Cap on concurrent Carbonyl sessions process-wide (0 = unlimited)"},
	{KeyCarbonylMaxPerIP, TypeInt, "Cap on concurrent Carbonyl sessions per source IP (0 = unlimited)"},
	{KeyCarbonylMaxPerHandle, TypeInt, "Cap on concurrent Carbonyl sessions per user handle (0 = unlimited)"},
}

// Snapshot is an immutable, strongly-typed view of every known setting. Cache
// stores *Snapshot via atomic.Pointer and replaces it wholesale on refresh.
// Snapshot fields are zero-valued for unset settings UNLESS a Default was
// provided at construction (the common case).
type Snapshot struct {
	SignupsEnabled         bool
	SignupsDisabledMessage string
	MOTD                   string
	WallEnabled            bool
	MaxTotalSessions       int
	MaxConnPerIP           int
	MaxUnauthHandshakes    int
	LockoutHandleThreshold int
	LockoutIPThreshold     int
	LockoutWindowSeconds   int
	CarbonylEnabled        bool
	CarbonylMaxGlobal      int
	CarbonylMaxPerIP       int
	CarbonylMaxPerHandle   int
}

// Defaults is the fallback Snapshot for unset rows. main.go builds this from
// config.Options so existing env-driven deployments keep their behavior — a
// brand-new install with no system_settings rows reads identically to the
// pre-settings build.
//
// settings doesn't import config to avoid an import cycle (auth.CreateAccount
// imports settings; config imports auth). main.go bridges the two.
type Defaults struct {
	SignupsEnabled         bool
	SignupsDisabledMessage string
	MOTD                   string
	WallEnabled            bool
	MaxTotalSessions       int
	MaxConnPerIP           int
	MaxUnauthHandshakes    int
	LockoutHandleThreshold int
	LockoutIPThreshold     int
	LockoutWindowSeconds   int
	CarbonylEnabled        bool
	CarbonylMaxGlobal      int
	CarbonylMaxPerIP       int
	CarbonylMaxPerHandle   int
}

// Cache is the in-process snapshot owner. One per process. Construct with
// NewCache, call Load before serving any traffic, then go cache.Run(ctx) for
// the periodic refresh + pub/sub listener.
type Cache struct {
	queries         *gen.Queries
	redisClient     *redis.Client
	logger          *slog.Logger
	refreshInterval time.Duration
	defaults        Defaults

	current atomic.Pointer[Snapshot]

	mu       sync.RWMutex
	onChange []func(Snapshot)
}

// NewCache builds an empty cache pre-seeded with the defaults snapshot. Call
// Load to overlay any persisted rows before serving traffic.
func NewCache(q *gen.Queries, rc *redis.Client, logger *slog.Logger, defaults Defaults, refreshInterval time.Duration) *Cache {
	if refreshInterval <= 0 {
		refreshInterval = 30 * time.Second
	}
	c := &Cache{
		queries:         q,
		redisClient:     rc,
		logger:          logger,
		refreshInterval: refreshInterval,
		defaults:        defaults,
	}
	snap := c.snapshotFromRows(nil)
	c.current.Store(&snap)
	return c
}

// Get returns the current snapshot. atomic.Pointer load + struct copy; safe to
// call on every check.
func (c *Cache) Get() Snapshot {
	p := c.current.Load()
	if p == nil {
		return c.snapshotFromRows(nil)
	}
	return *p
}

// OnChange registers a callback fired whenever the snapshot changes. Used by
// netlimit.Tracker to push new caps without polling. The callback is also
// invoked immediately with the current snapshot so the consumer can sync
// its initial state.
func (c *Cache) OnChange(f func(Snapshot)) {
	c.mu.Lock()
	c.onChange = append(c.onChange, f)
	c.mu.Unlock()
	f(c.Get())
}

// Load performs the synchronous initial refresh. main calls this before the
// listeners start so the very first request sees the persisted overrides.
func (c *Cache) Load(ctx context.Context) error {
	return c.refresh(ctx)
}

// Run drives the periodic refresh ticker + pub/sub invalidation listener.
// Returns when ctx is cancelled.
func (c *Cache) Run(ctx context.Context) {
	go c.listenInvalidations(ctx)
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refresh(ctx); err != nil {
				c.logger.Warn("settings cache refresh", "err", err)
			}
		}
	}
}

// Set writes a single key, validating against Catalog. On success it triggers
// a local refresh + publishes invalidation so other replicas re-read promptly.
// actorID is the sysop user id (nil for system writes — this path isn't used
// today but allows future callers to write programmatically).
func (c *Cache) Set(ctx context.Context, key, value string, actorID *int64) error {
	def, ok := lookupDef(key)
	if !ok {
		return fmt.Errorf("settings: unknown key %q", key)
	}
	if err := validate(def.Type, value); err != nil {
		return err
	}
	if err := c.queries.UpsertSystemSetting(ctx, gen.UpsertSystemSettingParams{
		Key:       key,
		Value:     value,
		Type:      def.Type,
		UpdatedBy: actorID,
	}); err != nil {
		return fmt.Errorf("settings: upsert %s: %w", key, err)
	}
	if err := c.refresh(ctx); err != nil {
		c.logger.Warn("settings: refresh after set", "err", err)
	}
	c.publish(ctx, key)
	return nil
}

// Reset deletes a key, falling back to the Defaults value. Returns the number
// of rows deleted (0 if no override existed).
func (c *Cache) Reset(ctx context.Context, key string) (int64, error) {
	if _, ok := lookupDef(key); !ok {
		return 0, fmt.Errorf("settings: unknown key %q", key)
	}
	n, err := c.queries.DeleteSystemSetting(ctx, key)
	if err != nil {
		return 0, err
	}
	if err := c.refresh(ctx); err != nil {
		c.logger.Warn("settings: refresh after reset", "err", err)
	}
	c.publish(ctx, key)
	return n, nil
}

// DefaultString returns the default value for a key as the string the UI
// renders. Used by the Settings tab to show "default: X" next to each row.
func (c *Cache) DefaultString(key string) string {
	snap := Snapshot{
		SignupsEnabled:         c.defaults.SignupsEnabled,
		SignupsDisabledMessage: c.defaults.SignupsDisabledMessage,
		MOTD:                   c.defaults.MOTD,
		WallEnabled:            c.defaults.WallEnabled,
		MaxTotalSessions:       c.defaults.MaxTotalSessions,
		MaxConnPerIP:           c.defaults.MaxConnPerIP,
		MaxUnauthHandshakes:    c.defaults.MaxUnauthHandshakes,
		LockoutHandleThreshold: c.defaults.LockoutHandleThreshold,
		LockoutIPThreshold:     c.defaults.LockoutIPThreshold,
		LockoutWindowSeconds:   c.defaults.LockoutWindowSeconds,
		CarbonylEnabled:        c.defaults.CarbonylEnabled,
		CarbonylMaxGlobal:      c.defaults.CarbonylMaxGlobal,
		CarbonylMaxPerIP:       c.defaults.CarbonylMaxPerIP,
		CarbonylMaxPerHandle:   c.defaults.CarbonylMaxPerHandle,
	}
	return snap.String(key)
}

func (c *Cache) refresh(ctx context.Context) error {
	rows, err := c.queries.ListSystemSettings(ctx)
	if err != nil {
		return fmt.Errorf("settings refresh: %w", err)
	}
	snap := c.snapshotFromRows(rows)
	c.current.Store(&snap)
	c.fireOnChange(snap)
	return nil
}

func (c *Cache) fireOnChange(snap Snapshot) {
	c.mu.RLock()
	cbs := make([]func(Snapshot), len(c.onChange))
	copy(cbs, c.onChange)
	c.mu.RUnlock()
	for _, cb := range cbs {
		cb(snap)
	}
}

func (c *Cache) snapshotFromRows(rows []gen.SystemSetting) Snapshot {
	snap := Snapshot{
		SignupsEnabled:         c.defaults.SignupsEnabled,
		SignupsDisabledMessage: c.defaults.SignupsDisabledMessage,
		MOTD:                   c.defaults.MOTD,
		WallEnabled:            c.defaults.WallEnabled,
		MaxTotalSessions:       c.defaults.MaxTotalSessions,
		MaxConnPerIP:           c.defaults.MaxConnPerIP,
		MaxUnauthHandshakes:    c.defaults.MaxUnauthHandshakes,
		LockoutHandleThreshold: c.defaults.LockoutHandleThreshold,
		LockoutIPThreshold:     c.defaults.LockoutIPThreshold,
		LockoutWindowSeconds:   c.defaults.LockoutWindowSeconds,
		CarbonylEnabled:        c.defaults.CarbonylEnabled,
		CarbonylMaxGlobal:      c.defaults.CarbonylMaxGlobal,
		CarbonylMaxPerIP:       c.defaults.CarbonylMaxPerIP,
		CarbonylMaxPerHandle:   c.defaults.CarbonylMaxPerHandle,
	}
	for _, r := range rows {
		c.applyRow(&snap, r.Key, r.Value)
	}
	return snap
}

func (c *Cache) applyRow(s *Snapshot, key, value string) {
	switch key {
	case KeySignupsEnabled:
		if b, err := strconv.ParseBool(value); err == nil {
			s.SignupsEnabled = b
		}
	case KeySignupsDisabledMessage:
		s.SignupsDisabledMessage = value
	case KeyMOTD:
		s.MOTD = value
	case KeyWallEnabled:
		if b, err := strconv.ParseBool(value); err == nil {
			s.WallEnabled = b
		}
	case KeyMaxTotalSessions:
		if n, err := strconv.Atoi(value); err == nil {
			s.MaxTotalSessions = n
		}
	case KeyMaxConnPerIP:
		if n, err := strconv.Atoi(value); err == nil {
			s.MaxConnPerIP = n
		}
	case KeyMaxUnauthHandshakes:
		if n, err := strconv.Atoi(value); err == nil {
			s.MaxUnauthHandshakes = n
		}
	case KeyLockoutHandleThreshold:
		if n, err := strconv.Atoi(value); err == nil {
			s.LockoutHandleThreshold = n
		}
	case KeyLockoutIPThreshold:
		if n, err := strconv.Atoi(value); err == nil {
			s.LockoutIPThreshold = n
		}
	case KeyLockoutWindowSeconds:
		if n, err := strconv.Atoi(value); err == nil {
			s.LockoutWindowSeconds = n
		}
	case KeyCarbonylEnabled:
		if b, err := strconv.ParseBool(value); err == nil {
			s.CarbonylEnabled = b
		}
	case KeyCarbonylMaxGlobal:
		if n, err := strconv.Atoi(value); err == nil {
			s.CarbonylMaxGlobal = n
		}
	case KeyCarbonylMaxPerIP:
		if n, err := strconv.Atoi(value); err == nil {
			s.CarbonylMaxPerIP = n
		}
	case KeyCarbonylMaxPerHandle:
		if n, err := strconv.Atoi(value); err == nil {
			s.CarbonylMaxPerHandle = n
		}
	default:
		c.logger.Warn("settings cache: unknown key in db", "key", key)
	}
}

func (c *Cache) publish(ctx context.Context, key string) {
	if c.redisClient == nil {
		return
	}
	if err := c.redisClient.Publish(ctx, invalidateChannel, key).Err(); err != nil {
		c.logger.Warn("settings cache publish", "key", key, "err", err)
	}
}

func (c *Cache) listenInvalidations(ctx context.Context) {
	if c.redisClient == nil {
		return
	}
	pubsub := c.redisClient.Subscribe(ctx, invalidateChannel)
	defer pubsub.Close()
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := c.refresh(ctx); err != nil {
				c.logger.Warn("settings invalidate refresh", "trigger", msg.Payload, "err", err)
			}
		}
	}
}

func validate(typ, value string) error {
	switch typ {
	case TypeBool:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("settings: value %q is not a bool", value)
		}
	case TypeInt:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("settings: value %q is not an int", value)
		}
	case TypeString:
		// any string is allowed
	default:
		return fmt.Errorf("settings: unknown type %q", typ)
	}
	return nil
}

func lookupDef(key string) (SettingDef, bool) {
	for _, d := range Catalog {
		if d.Key == key {
			return d, true
		}
	}
	return SettingDef{}, false
}

// String returns the snapshot's value for a key in the same stringified form
// the storage layer uses. Used by the sysop UI for tabular display and by
// audit-log "old → new" records.
func (s Snapshot) String(key string) string {
	switch key {
	case KeySignupsEnabled:
		return strconv.FormatBool(s.SignupsEnabled)
	case KeySignupsDisabledMessage:
		return s.SignupsDisabledMessage
	case KeyMOTD:
		return s.MOTD
	case KeyWallEnabled:
		return strconv.FormatBool(s.WallEnabled)
	case KeyMaxTotalSessions:
		return strconv.Itoa(s.MaxTotalSessions)
	case KeyMaxConnPerIP:
		return strconv.Itoa(s.MaxConnPerIP)
	case KeyMaxUnauthHandshakes:
		return strconv.Itoa(s.MaxUnauthHandshakes)
	case KeyLockoutHandleThreshold:
		return strconv.Itoa(s.LockoutHandleThreshold)
	case KeyLockoutIPThreshold:
		return strconv.Itoa(s.LockoutIPThreshold)
	case KeyLockoutWindowSeconds:
		return strconv.Itoa(s.LockoutWindowSeconds)
	case KeyCarbonylEnabled:
		return strconv.FormatBool(s.CarbonylEnabled)
	case KeyCarbonylMaxGlobal:
		return strconv.Itoa(s.CarbonylMaxGlobal)
	case KeyCarbonylMaxPerIP:
		return strconv.Itoa(s.CarbonylMaxPerIP)
	case KeyCarbonylMaxPerHandle:
		return strconv.Itoa(s.CarbonylMaxPerHandle)
	}
	return ""
}
