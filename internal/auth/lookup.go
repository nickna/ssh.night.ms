package auth

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
	"github.com/nickna/ssh.night.ms/internal/security/netlimit"
)

// CredentialProviderSSH is the value of the identity_credentials.provider column
// that the .NET stack writes for SSH keys. The .NET project stores the enum as
// its string name (CredentialProvider.Ssh → "Ssh") via
// .HasConversion<string>().HasMaxLength(32) in AppDbContext. We match exactly.
const CredentialProviderSSH = "Ssh"

// Lookup runs the publickey / password decision tree shared by the SSH transport
// and (later) the web /login/password endpoint. Mirrors src/Night.Ms.SshServer/
// Auth/AuthLookupService.cs from the .NET project.
//
// Lifetime: process-singleton. Stateless; holds a pgxpool + hasher + rate
// limiter + (optional) persistent-ban cache.
type Lookup struct {
	Pool    *pgxpool.Pool
	Queries *gen.Queries
	Hasher  *Hasher
	Limiter RateLimiter
	Logger  *slog.Logger

	// Bans, when non-nil, is consulted first on every auth attempt. A
	// matching active ban short-circuits to Banned regardless of credential
	// validity — once an IP is on the persistent list, even a valid pubkey
	// can't get in until the ban expires or a sysop revokes it.
	Bans *BanCache

	// Audit, when non-nil, receives an AuthSuccess / AuthFailure event for
	// every terminal decision out of ByPublicKey / ByPassword. Nil here is
	// the default for unit tests; production wires audit.NewPostgresRecorder.
	Audit audit.Recorder
}

// ByPublicKey is the publickey auth path. Handle comes from the SSH username,
// fingerprint is SHA256:base64(sha256(blob)) — the format produced by
// golang.org/x/crypto/ssh.FingerprintSHA256, which matches what the .NET
// OpenSshPublicKeyParser computes.
//
// sourceIP is consulted by the optional BanCache to short-circuit known-bad
// IPs even when they present a valid key. Pass nil to skip the ban check
// (e.g., from tests that don't simulate a network address).
//
// The named return `decision` lets the deferred audit emit see whatever the
// body returns without rewriting every `return` site — the audit wiring is
// purely additive.
func (l *Lookup) ByPublicKey(ctx context.Context, handle, fingerprint, algorithm string, blob []byte, sourceIP net.Addr) (decision Decision) {
	defer func() { l.emitAuthAudit(ctx, "publickey", handle, sourceIP, decision) }()
	if d := l.checkPersistentBan(sourceIP); d != nil {
		return d
	}
	if handle == "" {
		return Refused{Reason: "empty handle"}
	}

	user, err := l.Queries.GetUserByHandle(ctx, handle)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Unknown handle + offered key → signup flow with key carry-through.
			return SignupRequired{
				Handle:             handle,
				OfferedFingerprint: fingerprint,
				OfferedAlgorithm:   algorithm,
				OfferedBlob:        blob,
			}
		}
		l.Logger.Error("publickey user lookup", "handle", handle, "err", err)
		return Refused{Reason: "internal error"}
	}

	if user.IsBanned {
		return Banned{Reason: "account is banned"}
	}

	cred, err := l.Queries.GetCredentialByProviderSubject(ctx, gen.GetCredentialByProviderSubjectParams{
		Provider: CredentialProviderSSH,
		Subject:  fingerprint,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Key not registered to anyone. Refuse so the client can try password;
			// the offered-key info travels forward via the Known decision later if
			// the password attempt succeeds.
			return Refused{Reason: "key not registered to this account"}
		}
		l.Logger.Error("publickey credential lookup", "fingerprint", fingerprint, "err", err)
		return Refused{Reason: "internal error"}
	}
	if cred.UserID != user.ID {
		return Refused{Reason: "key not registered to this account"}
	}

	// Touch last_used_at fire-and-forget. We don't block the auth response on this.
	go func(id int64) {
		ctxBG, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.Queries.TouchCredentialLastUsed(ctxBG, gen.TouchCredentialLastUsedParams{
			ID:         id,
			LastUsedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		}); err != nil {
			l.Logger.Warn("touch credential last_used_at", "err", err)
		}
	}(cred.ID)

	return Known{
		UserID:  user.ID,
		Handle:  user.Handle,
		IsSysop: user.IsSysop,
	}
}

// ByPassword is the password auth path. Always burns one Argon2id evaluation of
// wall time on the failure paths (unknown user, no-password, banned, rate-
// limited, wrong password) so wall-clock timing leaks no information about
// account state.
func (l *Lookup) ByPassword(ctx context.Context, handle, secret string, sourceIP net.Addr) (decision Decision) {
	defer func() { l.emitAuthAudit(ctx, "password", handle, sourceIP, decision) }()
	if d := l.checkPersistentBan(sourceIP); d != nil {
		// Burn the dummy so wall-time on the banned path matches the
		// normal-failure path. An attacker who knows their IP is banned
		// can't probe whether the cache caught it by timing.
		l.Hasher.VerifyDummy(secret)
		return d
	}
	if handle == "" {
		// Empty handles still pay the dummy Argon2id so wall-time matches
		// the populated path, AND still bump the per-IP failure counter so
		// a spray of empty-handle attempts trips the IP lockout rather than
		// free-rolling against the limiter (RecordFailure with handle=""
		// is a no-op for the handle counter but increments the IP one).
		l.Hasher.VerifyDummy(secret)
		_ = l.Limiter.RecordFailure(ctx, "", sourceIP)
		return Refused{Reason: "empty handle"}
	}

	// Rate-limit first. Apply to all attempts including unknown handles so attackers
	// can't free-pass via spraying nonexistent usernames.
	rl, err := l.Limiter.Check(ctx, handle, sourceIP)
	if err != nil {
		l.Logger.Warn("rate limiter check failed", "handle", handle, "err", err)
	}
	if rl.LockedOut {
		l.Hasher.VerifyDummy(secret)
		return RateLimited{RetryAfter: rl.RetryAfter}
	}

	user, err := l.Queries.GetUserByHandle(ctx, handle)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			l.Hasher.VerifyDummy(secret)
			_ = l.Limiter.RecordFailure(ctx, handle, sourceIP)
			return SignupRequired{Handle: handle, OfferedPassword: secret}
		}
		l.Logger.Error("password user lookup", "handle", handle, "err", err)
		l.Hasher.VerifyDummy(secret)
		return Refused{Reason: "internal error"}
	}

	if user.IsBanned {
		l.Hasher.VerifyDummy(secret)
		return Banned{Reason: "account is banned"}
	}

	if len(user.PasswordHash) == 0 {
		l.Hasher.VerifyDummy(secret)
		_ = l.Limiter.RecordFailure(ctx, handle, sourceIP)
		l.Logger.Info("password attempt for handle with no password set", "handle", user.Handle)
		return Refused{Reason: "no password set for this account"}
	}

	algo := ""
	if user.PasswordAlgo != nil {
		algo = *user.PasswordAlgo
	}
	res := l.Hasher.Verify(secret, user.PasswordHash, algo)
	if !res.OK {
		_ = l.Limiter.RecordFailure(ctx, handle, sourceIP)
		l.Logger.Info("password verify failed", "handle", user.Handle)
		return Refused{Reason: "invalid password"}
	}

	// RequireSshKey ("passwordless mode") rejects password auth even with a valid
	// password. Check AFTER the verify so wrong-password and right-but-blocked
	// attempts both pay one full Argon2id verify. Counts as a failure for rate
	// limiting so brute force still tips the limiter.
	if user.RequireSshKey {
		_ = l.Limiter.RecordFailure(ctx, handle, sourceIP)
		l.Logger.Info("password verify succeeded but RequireSshKey is on; refusing",
			"handle", user.Handle)
		return Refused{Reason: "account requires SSH key authentication"}
	}

	_ = l.Limiter.Clear(ctx, handle)

	// Lazy migrate to PHC (or rehash with current params if PHC drifted).
	if res.NeedsRehash {
		go l.rehashAsync(user.ID, user.Handle, secret)
	}

	return Known{
		UserID:  user.ID,
		Handle:  user.Handle,
		IsSysop: user.IsSysop,
	}
}

// emitAuthAudit translates a terminal Decision into the matching audit Event
// and forwards it to the recorder. The handle argument is the caller-
// supplied SSH username (may be empty); for Known we prefer the canonical
// handle on the decision so the audit row matches the users table.
// SignupRequired is intentionally not emitted — it's an onboarding signal,
// not a security event, and emitting it would clutter the feed.
func (l *Lookup) emitAuthAudit(ctx context.Context, method, handle string, sourceIP net.Addr, decision Decision) {
	if l.Audit == nil || decision == nil {
		return
	}
	ip := ""
	if sourceIP != nil {
		ip = netlimit.CollapseIP(sourceIP)
	}
	switch d := decision.(type) {
	case Known:
		l.Audit.Record(ctx, audit.AuthSuccess{Handle: d.Handle, IP: ip, Method: method})
	case SignupRequired:
		// no-op
	case Banned:
		l.Audit.Record(ctx, audit.AuthFailure{
			Handle: handle, IP: ip, Method: method, Reason: "banned: " + d.Reason,
		})
	case RateLimited:
		l.Audit.Record(ctx, audit.AuthFailure{
			Handle: handle, IP: ip, Method: method, Reason: "rate-limited",
		})
	case Refused:
		l.Audit.Record(ctx, audit.AuthFailure{
			Handle: handle, IP: ip, Method: method, Reason: d.Reason,
		})
	}
}

// checkPersistentBan consults the BanCache (if wired) and returns a Banned
// decision when sourceIP — collapsed to its canonical key — has an active
// row in security_ip_bans. Returns nil to mean "no ban; proceed". Centralized
// here so both ByPublicKey and ByPassword share the same gate.
func (l *Lookup) checkPersistentBan(sourceIP net.Addr) Decision {
	if l.Bans == nil || sourceIP == nil {
		return nil
	}
	key := netlimit.CollapseIP(sourceIP)
	if key == "" {
		return nil
	}
	if banned, _ := l.Bans.IsBanned(key); banned {
		return Banned{Reason: "ip persistently banned"}
	}
	return nil
}

// rehashAsync re-hashes a password with current params and writes it back. Runs
// fire-and-forget on a background ctx so we don't block the auth response.
func (l *Lookup) rehashAsync(userID int64, handle, password string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fresh, algo, err := l.Hasher.Hash(password)
	if err != nil {
		l.Logger.Error("rehash hash failed", "handle", handle, "err", err)
		return
	}
	// algo is empty for PHC. Store as NULL via a nil *string. UpdateUserPassword
	// takes *string for PasswordAlgo so we just pass &algo when non-empty.
	var algoPtr *string
	if algo != "" {
		algoPtr = &algo
	}
	if err := l.Queries.UpdateUserPassword(ctx, gen.UpdateUserPasswordParams{
		ID:                userID,
		PasswordHash:      fresh,
		PasswordAlgo:      algoPtr,
		PasswordUpdatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		l.Logger.Error("rehash update failed", "handle", handle, "err", err)
		return
	}
	l.Logger.Info("rehashed password to PHC", "handle", handle)
}
