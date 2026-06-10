// Package audit emits structured security events to two sinks: a Postgres
// table (security_events) consumed by the sysop UI events feed, and a slog
// JSON stream consumed by external log shippers / fail2ban. Events are
// concrete structs satisfying the Event interface so each event's details
// payload is well-shaped (no stringly-typed kitchen-sink map).
//
// Hot path semantics: the Recorder NEVER blocks the caller. Postgres writes
// go through a buffered channel + background goroutine; on buffer full the
// recorder drops the DB write (incrementing an expvar counter) but still
// emits the slog line synchronously. An auth-flood scenario should not be
// allowed to back-pressure the auth path into a self-inflicted DoS.
package audit

import (
	"time"
)

// Severity levels. info = routine (auth success, sysop ban), warn = something
// noteworthy (lockout, manual ban, handshake failure), crit = something an
// operator should look at (persistent ban escalation, repeated rejects).
const (
	SeverityInfo = "info"
	SeverityWarn = "warn"
	SeverityCrit = "crit"
)

// Event is the contract every concrete audit event implements. EventType +
// Severity populate the matching columns in security_events; Subject returns
// the (handle, ip) pair — either may be empty when not applicable, in which
// case the SQL column is written as NULL. Details is marshalled to jsonb.
//
// Using a single Subject() instead of separate Handle()/IP() methods lets
// the concrete structs keep their natural field names (Handle, IP) without
// colliding with the interface methods.
type Event interface {
	EventType() string
	Severity() string
	Subject() (handle, ip string)
	Details() any
}

// AuthSuccess fires when ByPassword / ByPublicKey returns Known.
type AuthSuccess struct {
	Handle string
	IP     string
	Method string // "password" | "publickey"
}

func (e AuthSuccess) EventType() string         { return "auth_success" }
func (e AuthSuccess) Severity() string          { return SeverityInfo }
func (e AuthSuccess) Subject() (string, string) { return e.Handle, e.IP }
func (e AuthSuccess) Details() any              { return map[string]any{"method": e.Method} }

// AuthFailure fires on every Refused / RateLimited path on the auth pipeline.
// Reason captures the internal cause (kept for log triage); the SSH client
// receives only a generic rejection per the enumeration-mitigation choice.
type AuthFailure struct {
	Handle string
	IP     string
	Method string // "password" | "publickey"
	Reason string
}

func (e AuthFailure) EventType() string         { return "auth_failure" }
func (e AuthFailure) Severity() string          { return SeverityInfo }
func (e AuthFailure) Subject() (string, string) { return e.Handle, e.IP }
func (e AuthFailure) Details() any {
	return map[string]any{"method": e.Method, "reason": e.Reason}
}

// LockoutHandle fires when the per-handle failure counter trips its threshold
// and a lock is set. Lockcount is the post-INCR escalation counter — Duration
// is its computed lock duration.
type LockoutHandle struct {
	Handle    string
	IP        string
	Fails     int
	Lockcount int64
	Duration  time.Duration
}

func (e LockoutHandle) EventType() string         { return "lockout_handle" }
func (e LockoutHandle) Severity() string          { return SeverityWarn }
func (e LockoutHandle) Subject() (string, string) { return e.Handle, e.IP }
func (e LockoutHandle) Details() any {
	return map[string]any{
		"fails":      e.Fails,
		"lockcount":  e.Lockcount,
		"duration_s": e.Duration.Seconds(),
	}
}

// LockoutIP fires when the per-IP failure counter trips. Same fields as
// LockoutHandle minus the handle.
type LockoutIP struct {
	IP        string
	Fails     int
	Lockcount int64
	Duration  time.Duration
}

func (e LockoutIP) EventType() string         { return "lockout_ip" }
func (e LockoutIP) Severity() string          { return SeverityWarn }
func (e LockoutIP) Subject() (string, string) { return "", e.IP }
func (e LockoutIP) Details() any {
	return map[string]any{
		"fails":      e.Fails,
		"lockcount":  e.Lockcount,
		"duration_s": e.Duration.Seconds(),
	}
}

// PersistentBanAuto fires when an IP's lockcount crosses the threshold and
// the auto-promotion writes a security_ip_bans row. Distinct from
// PersistentBanManual so the sysop UI can colorize differently.
type PersistentBanAuto struct {
	IP        string
	Lockcount int64
	ExpiresAt time.Time
}

func (e PersistentBanAuto) EventType() string         { return "persistent_ban_auto" }
func (e PersistentBanAuto) Severity() string          { return SeverityCrit }
func (e PersistentBanAuto) Subject() (string, string) { return "", e.IP }
func (e PersistentBanAuto) Details() any {
	return map[string]any{
		"lockcount":  e.Lockcount,
		"expires_at": e.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// PersistentBanManual fires when a sysop issues `ban-ip`.
type PersistentBanManual struct {
	IP        string
	ByHandle  string
	Reason    string
	ExpiresAt time.Time
}

func (e PersistentBanManual) EventType() string         { return "persistent_ban_manual" }
func (e PersistentBanManual) Severity() string          { return SeverityWarn }
func (e PersistentBanManual) Subject() (string, string) { return e.ByHandle, e.IP }
func (e PersistentBanManual) Details() any {
	return map[string]any{
		"reason":     e.Reason,
		"expires_at": e.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// PersistentBanRevoke fires when a sysop issues `unban-ip` or the cleanup
// goroutine prunes an expired row. ByHandle is empty for the cleanup path.
type PersistentBanRevoke struct {
	IP       string
	ByHandle string
}

func (e PersistentBanRevoke) EventType() string         { return "persistent_ban_revoke" }
func (e PersistentBanRevoke) Severity() string          { return SeverityInfo }
func (e PersistentBanRevoke) Subject() (string, string) { return e.ByHandle, e.IP }
func (PersistentBanRevoke) Details() any                { return nil }

// ConnRejectedOverlimit fires when the netlimit Tracker refuses a connection
// (per-IP cap, token bucket, or global unauth cap).
type ConnRejectedOverlimit struct {
	IP     string
	Reason string // one of the netlimit.RejectReason values
}

func (e ConnRejectedOverlimit) EventType() string         { return "conn_rejected_overlimit" }
func (e ConnRejectedOverlimit) Severity() string          { return SeverityWarn }
func (e ConnRejectedOverlimit) Subject() (string, string) { return "", e.IP }
func (e ConnRejectedOverlimit) Details() any              { return map[string]any{"reason": e.Reason} }

// ConnRejectedBanned fires when the netlimit Tracker drops a connection at
// TCP-accept because the source IP is on the persistent-ban list — the ban
// enforced at the connection layer, before the SSH handshake, instead of
// only at the post-handshake auth callback. Distinct event_type from
// ConnRejectedOverlimit so the sysop feed and log shippers can tell a
// known-bad-offender drop apart from a capacity/rate rejection. Info
// severity, by design: dropping an already-banned IP is the system working
// as intended, and this is the highest-volume reject path (a banned bot
// reconnects continuously), so it must not flood the warn feed.
type ConnRejectedBanned struct {
	IP string
}

func (e ConnRejectedBanned) EventType() string         { return "conn_rejected_banned" }
func (e ConnRejectedBanned) Severity() string          { return SeverityInfo }
func (e ConnRejectedBanned) Subject() (string, string) { return "", e.IP }
func (ConnRejectedBanned) Details() any                { return nil }

// OAuthLinked fires when a user successfully attaches a Google or
// Microsoft account to their SSH identity. Method discriminates the entry
// path ("browser" via the /auth/{provider}/callback redirect, "device"
// via the TUI device-code flow) so the sysop UI can spot anomalies — a
// flurry of "browser" links from one IP range, etc.
type OAuthLinked struct {
	Handle          string
	IP              string
	Provider        string
	ProviderSubject string // the stable per-account ID at the IdP (Google sub, MS oid)
	Method          string // "browser" | "device"
}

func (e OAuthLinked) EventType() string         { return "oauth_linked" }
func (e OAuthLinked) Severity() string          { return SeverityInfo }
func (e OAuthLinked) Subject() (string, string) { return e.Handle, e.IP }
func (e OAuthLinked) Details() any {
	return map[string]any{"provider": e.Provider, "provider_subject": e.ProviderSubject, "method": e.Method}
}

// OAuthUnlinked fires when a user removes a linked OAuth account, either
// from the TUI profile screen or the web /profile/connections page.
type OAuthUnlinked struct {
	Handle          string
	IP              string
	Provider        string
	ProviderSubject string
}

func (e OAuthUnlinked) EventType() string         { return "oauth_unlinked" }
func (e OAuthUnlinked) Severity() string          { return SeverityInfo }
func (e OAuthUnlinked) Subject() (string, string) { return e.Handle, e.IP }
func (e OAuthUnlinked) Details() any {
	return map[string]any{"provider": e.Provider, "provider_subject": e.ProviderSubject}
}

// OAuthRefreshed fires from the background refresher when an access
// token is successfully renewed. Routine — info severity — but the
// before/after timestamps are useful for diagnosing slow drift.
type OAuthRefreshed struct {
	Provider        string
	ProviderSubject string
	PrevExpiresAt   time.Time
	NewExpiresAt    time.Time
}

func (e OAuthRefreshed) EventType() string         { return "oauth_refreshed" }
func (e OAuthRefreshed) Severity() string          { return SeverityInfo }
func (e OAuthRefreshed) Subject() (string, string) { return "", "" }
func (e OAuthRefreshed) Details() any {
	return map[string]any{
		"provider":         e.Provider,
		"provider_subject": e.ProviderSubject,
		"prev_expires_at":  e.PrevExpiresAt.UTC().Format(time.RFC3339),
		"new_expires_at":   e.NewExpiresAt.UTC().Format(time.RFC3339),
	}
}

// OAuthRefreshFailed fires on a transient refresh failure (provider 5xx,
// 429, network blip). The refresher bumps refresh_failure_count without
// flipping needs_reauth until the count crosses the threshold — so a
// single emit here is informational, not actionable.
type OAuthRefreshFailed struct {
	Provider        string
	ProviderSubject string
	ErrCode         string
	ErrMsg          string
	AttemptCount    int
}

func (e OAuthRefreshFailed) EventType() string         { return "oauth_refresh_failed" }
func (e OAuthRefreshFailed) Severity() string          { return SeverityWarn }
func (e OAuthRefreshFailed) Subject() (string, string) { return "", "" }
func (e OAuthRefreshFailed) Details() any {
	return map[string]any{
		"provider":         e.Provider,
		"provider_subject": e.ProviderSubject,
		"err_code":         e.ErrCode,
		"err_msg":          e.ErrMsg,
		"attempt_n":        e.AttemptCount,
	}
}

// OAuthReauthRequired fires when the refresher gives up on a row — either
// the provider returned invalid_grant (refresh token revoked or expired)
// or the soft-failure counter crossed the threshold. The token row gets
// needs_reauth=true and the profile UI surfaces a "re-authorize" badge.
type OAuthReauthRequired struct {
	Provider        string
	ProviderSubject string
	Reason          string
}

func (e OAuthReauthRequired) EventType() string         { return "oauth_reauth_required" }
func (e OAuthReauthRequired) Severity() string          { return SeverityWarn }
func (e OAuthReauthRequired) Subject() (string, string) { return "", "" }
func (e OAuthReauthRequired) Details() any {
	return map[string]any{
		"provider":         e.Provider,
		"provider_subject": e.ProviderSubject,
		"reason":           e.Reason,
	}
}

// HandshakeFailed fires from the SSH server's ConnectionFailedCallback —
// captures bad MACs, version-string scanners, slowloris-timed-out conns,
// protocol-downgrade probes. None of these reach the auth callbacks, so
// without this event they would be invisible in the security feed.
type HandshakeFailed struct {
	IP  string
	Err string
}

func (e HandshakeFailed) EventType() string         { return "handshake_failed" }
func (e HandshakeFailed) Severity() string          { return SeverityInfo }
func (e HandshakeFailed) Subject() (string, string) { return "", e.IP }
func (e HandshakeFailed) Details() any              { return map[string]any{"err": e.Err} }
