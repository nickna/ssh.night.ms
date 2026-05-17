using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Auth;

// Single-entry dispatcher for the AuthQuery discriminated union. The SSH transport calls
// LookupAsync once per auth attempt; this class dispatches on the concrete subtype and
// returns the corresponding AuthDecision shape. Lifetime is Singleton — the SSH transport
// has no DI scope around the callback, so collaborators (IDbContextFactory, IPasswordHasher,
// ILoginRateLimiter) must also be Singleton-compatible.
//
// Auth-flow rules:
//   - Username lookup is by handle (citext, case-insensitive).
//   - Banned users always return Banned, regardless of credential validity.
//   - Unknown handles always return SignupRequired (TUI handles the rest).
//   - Publickey auth requires the offered fingerprint to be registered to the named handle.
//     If not, return null → DevTunnels advertises Password as the next method.
//   - Password auth always runs an Argon2id verify, even for unknown users or users with no
//     password set, so wall-clock time leaks no information about user existence.
//   - Rate-limit check runs before any verify on Password attempts. RateLimited short-
//     circuits the verify entirely.
//   - On successful password auth, clear the per-handle failure counter; on failure,
//     increment both handle and IP counters.
public sealed class AuthLookupService(
    IDbContextFactory<AppDbContext> dbFactory,
    IPasswordHasher hasher,
    ILoginRateLimiter rateLimiter,
    ILogger<AuthLookupService> logger)
{
    public Task<AuthDecision> LookupAsync(AuthQuery query, CancellationToken ct) => query switch
    {
        AuthQuery.PublicKeyQuery q => HandlePublicKeyQueryAsync(q, ct),
        AuthQuery.PublicKey q => HandlePublicKeyAsync(q, ct),
        AuthQuery.Password q => HandlePasswordAsync(q, ct),
        AuthQuery.None q => HandleNoneAsync(q, ct),
        _ => Task.FromResult<AuthDecision>(new AuthDecision.SignupRequired(string.Empty)),
    };

    // Phase 1 of publickey auth: client asks "would you accept this key?" before signing.
    // Same logic as the actual key auth — if the key is registered to the named handle,
    // accept; otherwise return SignupRequired (for unknown handles) or fail.
    private async Task<AuthDecision> HandlePublicKeyQueryAsync(AuthQuery.PublicKeyQuery q, CancellationToken ct)
        => await EvaluateKeyAsync(q.Handle, q.Fingerprint, q.Algorithm, q.Blob, ct);

    private async Task<AuthDecision> HandlePublicKeyAsync(AuthQuery.PublicKey q, CancellationToken ct)
    {
        var decision = await EvaluateKeyAsync(q.Handle, q.Fingerprint, q.Algorithm, q.Blob, ct);
        if (decision is AuthDecision.Known known)
        {
            // Touch the credential's LastUsedAt on real (phase 2) auth.
            await TouchCredentialAsync(known.UserId, q.Fingerprint, ct);
        }
        return decision;
    }

    private async Task<AuthDecision> EvaluateKeyAsync(string handle, string fingerprint, string algorithm, byte[] blob, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);

        var user = await ResolveUserAsync(db, handle, ct);
        if (user is null)
        {
            // Unknown handle + offered key → signup with key adoption opportunity. The
            // transport layer carries the key blob through to the signup screen.
            return new AuthDecision.SignupRequired(handle, fingerprint, algorithm, blob);
        }
        if (user.IsBanned) return new AuthDecision.Banned("Account is banned.");

        // Look up the credential by (Provider, Subject) — same single-index hit as before —
        // and confirm it belongs to the named user.
        var credential = await db.IdentityCredentials
            .AsNoTracking()
            .FirstOrDefaultAsync(c => c.Provider == CredentialProvider.Ssh && c.Subject == fingerprint, ct);
        if (credential is null || credential.UserId != user.Id)
        {
            // Key isn't registered to this handle. Refused tells the transport "this
            // specific credential didn't authenticate; advertise the other methods so the
            // client can try password next." The offered fingerprint is still preserved on
            // the session via the transport's RejectRefused path so a successful password
            // login on the same session can surface the adopt-key prompt.
            return new AuthDecision.Refused("key not registered to this account");
        }

        return new AuthDecision.Known(user.Id, user.Handle, user.IsSysop,
            OfferedFingerprint: fingerprint, OfferedAlgorithm: algorithm, OfferedBlob: blob);
    }

    private async Task<AuthDecision> HandlePasswordAsync(AuthQuery.Password q, CancellationToken ct)
    {
        // Rate-limit first. Apply to all password attempts including unknown users — we
        // don't want to give a free-pass attack vector via "spray nonexistent handles".
        var check = await rateLimiter.CheckAsync(q.Handle, q.SourceIp, ct);
        if (check is RateLimitCheck.LockedOut locked)
        {
            // Still run a dummy verify so locked-out attempts take the same wall time as
            // unlocked ones — otherwise the lockout state itself is a side-channel.
            hasher.VerifyDummy(q.Secret);
            return new AuthDecision.RateLimited(locked.RetryAfter);
        }

        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var user = await ResolveUserAsync(db, q.Handle, ct);

        if (user is null)
        {
            // Burn time on the dummy hash to mask handle existence, then increment counters
            // and return signup. Recording a "failure" here would let an attacker enumerate
            // valid handles by counting which usernames produce a lockout vs not — instead,
            // unknown-handle attempts increment IP-only.
            hasher.VerifyDummy(q.Secret);
            await rateLimiter.RecordFailureAsync(q.Handle, q.SourceIp, ct);
            // Signup invitation. The TUI prefills the handle and asks the user to set a
            // password (the SSH-side password they just typed is NOT used — too easy to
            // accidentally bootstrap an account with a botched password).
            return new AuthDecision.SignupRequired(q.Handle);
        }

        if (user.IsBanned) return new AuthDecision.Banned("Account is banned.");

        if (user.PasswordHash is null || user.PasswordAlgo is null)
        {
            // User exists but has never set a password. Still burn the time, increment, and
            // refuse. Banner-style hint isn't possible without a separate Failed shape, so
            // failure goes through the standard channel — the web profile guides users to
            // either set a password or upload a key.
            hasher.VerifyDummy(q.Secret);
            await rateLimiter.RecordFailureAsync(q.Handle, q.SourceIp, ct);
            logger.LogInformation("Password attempt for handle={Handle} — no password set", user.Handle);
            return new AuthDecision.Refused("no password set for this account");
        }

        var ok = hasher.Verify(q.Secret, user.PasswordHash, user.PasswordAlgo);
        if (!ok)
        {
            await rateLimiter.RecordFailureAsync(q.Handle, q.SourceIp, ct);
            logger.LogInformation("Password verify failed for handle={Handle}", user.Handle);
            return new AuthDecision.Refused("invalid password");
        }

        // RequireSshKey ("passwordless mode") rejects password auth even with a valid
        // password. We check AFTER the verify, not before, so the wall-clock time of an
        // attempt against a passwordless account doesn't leak the toggle state: a wrong
        // password and a right-but-passwordless-blocked password both pay for one full
        // Argon2id verify. Record as a failure so brute-force still tips the rate limiter.
        if (user.RequireSshKey)
        {
            await rateLimiter.RecordFailureAsync(q.Handle, q.SourceIp, ct);
            logger.LogInformation("Password verify succeeded but RequireSshKey is on for handle={Handle}; refusing", user.Handle);
            return new AuthDecision.Refused("account requires SSH key authentication");
        }

        // Successful login. Clear the handle counter; optionally rehash if algo drift.
        await rateLimiter.ClearAsync(q.Handle, ct);
        if (hasher.NeedsRehash(user.PasswordAlgo))
        {
            var fresh = hasher.Hash(q.Secret);
            user.PasswordHash = fresh.Hash;
            user.PasswordAlgo = fresh.Algo;
            user.PasswordUpdatedAt = DateTimeOffset.UtcNow;
            await db.SaveChangesAsync(ct);
            logger.LogInformation("Rehashed password for handle={Handle} after algo drift", user.Handle);
        }
        return new AuthDecision.Known(user.Id, user.Handle, user.IsSysop);
    }

    private async Task<AuthDecision> HandleNoneAsync(AuthQuery.None q, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var user = await ResolveUserAsync(db, q.Handle, ct);
        if (user is null) return new AuthDecision.SignupRequired(q.Handle);
        if (user.IsBanned) return new AuthDecision.Banned("Account is banned.");
        // Known user submitting "none" — never accept; the client should try a real method.
        return new AuthDecision.Refused("authentication required");
    }

    private static Task<User?> ResolveUserAsync(AppDbContext db, string handle, CancellationToken ct) =>
        string.IsNullOrWhiteSpace(handle)
            ? Task.FromResult<User?>(null)
            : db.Users.FirstOrDefaultAsync(u => u.Handle == handle, ct);

    private async Task TouchCredentialAsync(long userId, string fingerprint, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var credential = await db.IdentityCredentials.FirstOrDefaultAsync(
            c => c.UserId == userId && c.Provider == CredentialProvider.Ssh && c.Subject == fingerprint, ct);
        if (credential is null) return;
        credential.LastUsedAt = DateTimeOffset.UtcNow;
        await db.SaveChangesAsync(ct);
    }
}
