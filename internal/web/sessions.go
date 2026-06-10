package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const sessionCookieName = "nightms_session"

// sidEntropyBytes is the random-byte count behind each session ID. 32 bytes
// → 256 bits of entropy → base64url-encoded to ~43 chars. Far beyond any
// guessing attack; matches what most production session stores ship with.
const sidEntropyBytes = 32

// errInvalidSession is the generic "cookie says nothing useful" sentinel
// the middleware swallows to continue unauthenticated. Returned for
// missing cookies, malformed cookies, Redis misses, and Redis errors —
// callers don't need to distinguish.
var errInvalidSession = errors.New("invalid session")

// sessionStore is the production session store backed by Redis. The
// browser cookie carries an opaque random session ID; the server-side
// row holds the user_id + capture metadata + sliding-expiration timer.
// Per-session revocation, "log out everywhere," and the active-sessions
// listing page all key off this layout.
//
// Data layout (key prefix bumped to "web:" so the prior STRING-shaped
// rows from commit 44a9a18 silently TTL out — existing users re-log
// in once after deploy):
//
//   web:session:{sid}   HASH  → uid, ua, ip, ts, seen
//   web:user:{user_id}  SET   → {sid, sid, …}
//
// Both keys carry the session timeout TTL. The per-user index makes
// ClearAllForUser cheap and powers the listing page. Sliding-expiration
// on Read refreshes both TTLs + bumps the row's `seen` field.
type sessionStore struct {
	redis   *redis.Client
	secure  bool
	timeout time.Duration
}

func newSessionStore(client *redis.Client, secure bool, timeout time.Duration) *sessionStore {
	if timeout == 0 {
		timeout = 30 * 24 * time.Hour
	}
	return &sessionStore{redis: client, secure: secure, timeout: timeout}
}

// newSID generates a fresh opaque session ID. crypto/rand failure is
// genuinely catastrophic (the OS RNG would have to be broken), so we
// surface it rather than fall back to anything weaker.
func newSID() (string, error) {
	b := make([]byte, sidEntropyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session: rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sessionKey(sid string) string  { return "web:session:" + sid }
func userIndexKey(uid int64) string { return "web:user:" + strconv.FormatInt(uid, 10) }

// SessionInfo is the listing-page row shape. UA is truncated to 200 chars
// before display (rare to see longer, and the column would dominate the
// page if we let it run); IP is whatever middleware.RealIP populated.
// Current is true for exactly one entry — the session the listing request
// itself is authenticated by.
type SessionInfo struct {
	SID       string
	UA        string
	IP        string
	CreatedAt time.Time
	LastSeen  time.Time
	Current   bool
}

// Set creates a fresh session row, indexes it under the user, and writes
// the cookie. Captures UA + IP from the request so the listing page can
// show the user something recognizable. Returns the new SID for audit-log
// purposes. Redis errors surface up — the caller (login handler) shows
// the login page with a generic error rather than swallowing silently.
func (s *sessionStore) Set(ctx context.Context, r *http.Request, w http.ResponseWriter, userID int64) (string, error) {
	sid, err := newSID()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Unix()
	ua := truncate(r.UserAgent(), 200)
	ip := remoteIP(r)
	pipe := s.redis.TxPipeline()
	pipe.HSet(ctx, sessionKey(sid), map[string]any{
		"uid":  strconv.FormatInt(userID, 10),
		"ua":   ua,
		"ip":   ip,
		"ts":   strconv.FormatInt(now, 10),
		"seen": strconv.FormatInt(now, 10),
	})
	pipe.Expire(ctx, sessionKey(sid), s.timeout)
	pipe.SAdd(ctx, userIndexKey(userID), sid)
	// Refresh the user-index TTL too so it doesn't outlive its members.
	// Re-applied on every Set; if all sessions for a user time out the
	// index decays alongside them.
	pipe.Expire(ctx, userIndexKey(userID), s.timeout)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("session: store: %w", err)
	}
	http.SetCookie(w, secureCookie(sessionCookieName, sid, s.secure, time.Now().Add(s.timeout)))
	return sid, nil
}

// Read returns the user_id for the cookie's SID, refreshing the TTL on
// hit (sliding expiration) and bumping the row's `seen` timestamp so the
// listing page shows accurate last-used times. Any failure — missing
// cookie, Redis miss, Redis error, WRONGTYPE (legacy STRING-shaped row
// from before this commit) — collapses to errInvalidSession so the
// middleware can treat them uniformly.
func (s *sessionStore) Read(ctx context.Context, r *http.Request) (int64, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return 0, errInvalidSession
	}
	sid := c.Value
	if sid == "" {
		return 0, errInvalidSession
	}
	raw, err := s.redis.HGet(ctx, sessionKey(sid), "uid").Result()
	if err != nil {
		return 0, errInvalidSession
	}
	userID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errInvalidSession
	}
	// Slide both TTLs + touch `seen` on success. Failures here log but
	// don't fail the request — a one-off Redis blip shouldn't bounce an
	// authenticated user back to /login.
	now := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	pipe := s.redis.Pipeline()
	pipe.HSet(ctx, sessionKey(sid), "seen", now)
	pipe.Expire(ctx, sessionKey(sid), s.timeout)
	pipe.Expire(ctx, userIndexKey(userID), s.timeout)
	_, _ = pipe.Exec(ctx)
	return userID, nil
}

// CurrentSID returns the SID carried by the request cookie, or "" when
// the cookie is missing / empty. Used by the listing page to mark the
// row that authenticated the listing request itself.
func (s *sessionStore) CurrentSID(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

// Clear deletes the session row for the SID carried by the request cookie
// (best-effort — missing cookie or already-gone row is a no-op) and
// expires the client cookie. Always expires the cookie even if the Redis
// delete fails; the user-visible outcome should always be "logged out".
func (s *sessionStore) Clear(ctx context.Context, r *http.Request, w http.ResponseWriter) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		sid := c.Value
		// Look up user_id so we can scrub the per-user index too. Errors
		// here mean the session row's already gone — proceed to cookie
		// expiry without complaint.
		if raw, err := s.redis.HGet(ctx, sessionKey(sid), "uid").Result(); err == nil {
			if uid, err := strconv.ParseInt(raw, 10, 64); err == nil {
				pipe := s.redis.Pipeline()
				pipe.Del(ctx, sessionKey(sid))
				pipe.SRem(ctx, userIndexKey(uid), sid)
				_, _ = pipe.Exec(ctx)
			}
		} else {
			// At minimum, try a blind DEL — the row may exist but the
			// user_id read raced with something. Per-user index decay
			// catches stragglers via TTL.
			_ = s.redis.Del(ctx, sessionKey(sid)).Err()
		}
	}
	http.SetCookie(w, expiredCookie(sessionCookieName, s.secure))
}

// ClearAllForUser revokes every session belonging to userID. Used by the
// "Log out everywhere" action + the post-password-change re-mint flow.
// The current request's cookie is NOT expired here — the caller should
// do that separately if they want the current browser kicked too.
func (s *sessionStore) ClearAllForUser(ctx context.Context, userID int64) error {
	sids, err := s.redis.SMembers(ctx, userIndexKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("session: list user sessions: %w", err)
	}
	if len(sids) == 0 {
		// Index might have decayed independently. DEL is a no-op then.
		return s.redis.Del(ctx, userIndexKey(userID)).Err()
	}
	pipe := s.redis.Pipeline()
	keys := make([]string, 0, len(sids))
	for _, sid := range sids {
		keys = append(keys, sessionKey(sid))
	}
	pipe.Del(ctx, keys...)
	pipe.Del(ctx, userIndexKey(userID))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("session: clear all: %w", err)
	}
	return nil
}

// List returns every active session for userID, sorted with the most
// recently used first. Stale index members (sid in the SET but the
// session HASH already gone) are filtered out — the SET is best-effort
// and TTL decay can race the HASH side. `currentSID` (typically read
// from the listing request's own cookie via CurrentSID) marks one row
// so the UI can highlight "this session."
func (s *sessionStore) List(ctx context.Context, userID int64, currentSID string) ([]SessionInfo, error) {
	sids, err := s.redis.SMembers(ctx, userIndexKey(userID)).Result()
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	out := make([]SessionInfo, 0, len(sids))
	for _, sid := range sids {
		fields, err := s.redis.HGetAll(ctx, sessionKey(sid)).Result()
		if err != nil || len(fields) == 0 {
			// Row already gone — drop the stale index entry. Best-effort,
			// ignore the SREM error.
			_ = s.redis.SRem(ctx, userIndexKey(userID), sid).Err()
			continue
		}
		info := SessionInfo{
			SID:     sid,
			UA:      fields["ua"],
			IP:      fields["ip"],
			Current: sid == currentSID,
		}
		if v, err := strconv.ParseInt(fields["ts"], 10, 64); err == nil {
			info.CreatedAt = time.Unix(v, 0).UTC()
		}
		if v, err := strconv.ParseInt(fields["seen"], 10, 64); err == nil {
			info.LastSeen = time.Unix(v, 0).UTC()
		}
		out = append(out, info)
	}
	// Most recently seen first — the user's current session usually sorts
	// to the top, but any row with newer activity beats it. Stable on
	// equal timestamps so the order is reproducible across renders.
	sortByLastSeenDesc(out)
	return out, nil
}

// Revoke deletes one specific session by SID, scoped to userID — the
// (uid match) guard means user A can't kill user B's session by guessing
// an SID. Returns no error when the session is already gone; the
// desired end-state ("not logged in") is reached either way.
func (s *sessionStore) Revoke(ctx context.Context, userID int64, sid string) error {
	// Confirm the row belongs to userID before deleting. If the HASH no
	// longer exists, treat as success.
	uidStr, err := s.redis.HGet(ctx, sessionKey(sid), "uid").Result()
	if err != nil {
		// Row gone; drop the index entry in case it survived.
		_ = s.redis.SRem(ctx, userIndexKey(userID), sid).Err()
		return nil
	}
	uid, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil || uid != userID {
		// Not their session. Don't leak the existence of the SID — just
		// return nil and let the caller's redirect treat it as success.
		return nil
	}
	pipe := s.redis.Pipeline()
	pipe.Del(ctx, sessionKey(sid))
	pipe.SRem(ctx, userIndexKey(userID), sid)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	return nil
}

// sortByLastSeenDesc sorts SessionInfo by LastSeen descending, stable.
// Inlined small-N insertion sort — a user's session count is bounded by
// the cookie timeout, typically single digits.
func sortByLastSeenDesc(s []SessionInfo) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].LastSeen.After(s[j-1].LastSeen); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// truncate trims a string to at most n runes. Used to defend against
// pathologically long User-Agent strings in the storage path and
// rendering path both.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// remoteIP extracts a usable client address from the request. RealIP
// middleware has already normalized X-Forwarded-For / X-Real-IP into
// RemoteAddr, so we just strip the trailing port.
func remoteIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		// Strip ":port" if present. Bracketed IPv6 keeps its brackets so
		// the result is still parseable downstream.
		return addr[:i]
	}
	return addr
}
