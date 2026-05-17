using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Auth;

// Outcome of presenting (provider, subject) to the identity store.
public abstract record IdentityResolution
{
    // (Provider, Subject) already maps to a User.
    public sealed record Existing(long UserId, string Handle, bool IsSysop) : IdentityResolution;

    // (Provider, Subject) is new, but the verified email matched an existing User. A new
    // IdentityCredential was attached to that user; the caller should sign them in.
    public sealed record LinkedToExisting(long UserId, string Handle, bool IsSysop, long CredentialId) : IdentityResolution;

    // No existing user. Caller should redirect to a handle-picker page; the SSO ticket stays
    // in the External cookie so the picker can complete the sign-up.
    public sealed record NewSignup(string? SuggestedHandle, string? Email, bool EmailVerified) : IdentityResolution;

    // The matched user is banned; the caller should reject the sign-in.
    public sealed record Banned(string Reason) : IdentityResolution;
}

public abstract record LinkOutcome
{
    public sealed record Linked(long CredentialId) : LinkOutcome;
    public sealed record AlreadyLinkedToYou : LinkOutcome;
    public sealed record AlreadyLinkedToOther : LinkOutcome;
}

public abstract record UnlinkOutcome
{
    public sealed record Removed : UnlinkOutcome;
    public sealed record NotFound : UnlinkOutcome;
    public sealed record RefusedLastCredential : UnlinkOutcome;
}

// Single source of truth for the read+side-effect path that turns an incoming SSO ticket
// (Provider + Subject + optional verified Email) into either a sign-in for an existing
// User, an auto-link onto an existing email-matched User, or an instruction to go to the
// handle-picker. Audit-log writes for `identity.linked` / `identity.signup` / `identity.unlinked`
// happen here so callers don't each repeat them.
public sealed class IdentityResolutionService(AppDbContext db)
{
    public async Task<IdentityResolution> ResolveAsync(
        CredentialProvider provider,
        string subject,
        string? email,
        bool emailVerified,
        IDictionary<string, string?>? extraMetadata,
        CancellationToken cancellationToken)
    {
        var existing = await db.IdentityCredentials
            .Include(c => c.User)
            .FirstOrDefaultAsync(c => c.Provider == provider && c.Subject == subject, cancellationToken);

        if (existing is not null)
        {
            var u = existing.User!;
            if (u.IsBanned) return new IdentityResolution.Banned("Account is banned.");
            existing.LastUsedAt = DateTimeOffset.UtcNow;
            await db.SaveChangesAsync(cancellationToken);
            return new IdentityResolution.Existing(u.Id, u.Handle, u.IsSysop);
        }

        // Auto-link path: incoming verified email matches an existing user's email. Both the
        // verification flag AND a non-empty email are required so we never link on an empty
        // string or on a provider that omits email_verified.
        if (emailVerified && !string.IsNullOrWhiteSpace(email))
        {
            var matchedUser = await db.Users
                .FirstOrDefaultAsync(u => u.Email == email, cancellationToken);

            if (matchedUser is not null)
            {
                if (matchedUser.IsBanned) return new IdentityResolution.Banned("Account is banned.");
                var credential = new IdentityCredential
                {
                    UserId = matchedUser.Id,
                    Provider = provider,
                    Subject = subject,
                    Metadata = BuildMetadata(email, emailVerified, extraMetadata),
                    CreatedAt = DateTimeOffset.UtcNow,
                    LastUsedAt = DateTimeOffset.UtcNow,
                };
                db.IdentityCredentials.Add(credential);
                db.AuditLogs.Add(new AuditLog
                {
                    ActorId = matchedUser.Id,
                    Action = "identity.linked",
                    TargetType = "identity_credential",
                    CreatedAt = DateTimeOffset.UtcNow,
                    Details = JsonSerializer.SerializeToDocument(new
                    {
                        provider = provider.ToString(),
                        by = "auto-link",
                        email,
                    }),
                });
                await db.SaveChangesAsync(cancellationToken);
                return new IdentityResolution.LinkedToExisting(matchedUser.Id, matchedUser.Handle, matchedUser.IsSysop, credential.Id);
            }
        }

        return new IdentityResolution.NewSignup(
            SuggestedHandle: SuggestHandle(email),
            Email: email,
            EmailVerified: emailVerified);
    }

    // Called from the onboarding handle-picker after the user submits a chosen handle. The
    // caller is responsible for validating the handle shape; this method validates uniqueness
    // and DOES the insert. Returns (userId, handle, isSysop) so the caller can sign in.
    public async Task<(long UserId, string Handle, bool IsSysop)?> CreateUserAndCredentialAsync(
        string handle,
        CredentialProvider provider,
        string subject,
        string? email,
        bool emailVerified,
        IDictionary<string, string?>? extraMetadata,
        Func<string, bool> isBootstrapHandle,
        CancellationToken cancellationToken)
    {
        var promoteToSysop = isBootstrapHandle(handle);
        var now = DateTimeOffset.UtcNow;
        var user = new User
        {
            Handle = handle,
            Email = emailVerified ? email : null,
            CreatedAt = now,
            LastSeenAt = now,
            IsSysop = promoteToSysop,
        };
        var credential = new IdentityCredential
        {
            User = user,
            Provider = provider,
            Subject = subject,
            Metadata = BuildMetadata(email, emailVerified, extraMetadata),
            CreatedAt = now,
            LastUsedAt = now,
        };
        db.Users.Add(user);
        db.IdentityCredentials.Add(credential);
        try
        {
            await db.SaveChangesAsync(cancellationToken);
        }
        catch (DbUpdateException)
        {
            // Handle collision (or, rarely, email collision if we attempt to write a verified
            // email that's already taken — let the caller redirect back to onboarding with
            // a "try another handle" message).
            return null;
        }

        db.AuditLogs.Add(new AuditLog
        {
            ActorId = null,
            Action = "identity.signup",
            TargetType = "user",
            TargetId = user.Id,
            CreatedAt = now,
            Details = JsonSerializer.SerializeToDocument(new
            {
                provider = provider.ToString(),
                email,
            }),
        });
        if (promoteToSysop)
        {
            db.AuditLogs.Add(new AuditLog
            {
                ActorId = null,
                Action = "sysop.bootstrap",
                TargetType = "user",
                TargetId = user.Id,
                CreatedAt = now,
            });
        }
        await db.SaveChangesAsync(cancellationToken);
        return (user.Id, user.Handle, user.IsSysop);
    }

    // /profile/link/{provider} — a logged-in user is adding another credential to their own
    // account. Refuses if the (provider, subject) is already attached to a different user.
    public async Task<LinkOutcome> LinkToUserAsync(
        long currentUserId,
        CredentialProvider provider,
        string subject,
        string? email,
        bool emailVerified,
        IDictionary<string, string?>? extraMetadata,
        CancellationToken cancellationToken)
    {
        var existing = await db.IdentityCredentials
            .FirstOrDefaultAsync(c => c.Provider == provider && c.Subject == subject, cancellationToken);
        if (existing is not null)
        {
            return existing.UserId == currentUserId
                ? new LinkOutcome.AlreadyLinkedToYou()
                : new LinkOutcome.AlreadyLinkedToOther();
        }

        var now = DateTimeOffset.UtcNow;
        var credential = new IdentityCredential
        {
            UserId = currentUserId,
            Provider = provider,
            Subject = subject,
            Metadata = BuildMetadata(email, emailVerified, extraMetadata),
            CreatedAt = now,
            LastUsedAt = now,
        };
        db.IdentityCredentials.Add(credential);
        db.AuditLogs.Add(new AuditLog
        {
            ActorId = currentUserId,
            Action = "identity.linked",
            TargetType = "identity_credential",
            CreatedAt = now,
            Details = JsonSerializer.SerializeToDocument(new
            {
                provider = provider.ToString(),
                by = "manual",
                email,
            }),
        });
        // Backfill User.Email if they didn't have one and this credential supplied a verified
        // one. Lets future auto-link by email work for them too.
        if (emailVerified && !string.IsNullOrWhiteSpace(email))
        {
            var user = await db.Users.FirstAsync(u => u.Id == currentUserId, cancellationToken);
            if (string.IsNullOrEmpty(user.Email))
            {
                user.Email = email;
            }
        }
        await db.SaveChangesAsync(cancellationToken);
        return new LinkOutcome.Linked(credential.Id);
    }

    public async Task<UnlinkOutcome> UnlinkAsync(long currentUserId, long credentialId, CancellationToken cancellationToken)
    {
        var credential = await db.IdentityCredentials
            .FirstOrDefaultAsync(c => c.Id == credentialId && c.UserId == currentUserId, cancellationToken);
        if (credential is null) return new UnlinkOutcome.NotFound();

        // Count both kinds of "way to log in": registered credentials (SSH key / OIDC) AND
        // a password if one is set. The user must retain at least one way to authenticate
        // after the removal — otherwise we'd lock them out of their own account. Without
        // the password check, a password-only user (no OIDC, only-SSH-key-then-removed)
        // could un-knot themselves and have no way back in.
        var remainingCredentials = await db.IdentityCredentials.CountAsync(c => c.UserId == currentUserId, cancellationToken);
        var hasPassword = await db.Users.AnyAsync(u => u.Id == currentUserId && u.PasswordHash != null, cancellationToken);
        var remainingAuthMethods = (remainingCredentials - 1) + (hasPassword ? 1 : 0);
        if (remainingAuthMethods < 1)
        {
            return new UnlinkOutcome.RefusedLastCredential();
        }

        db.IdentityCredentials.Remove(credential);
        db.AuditLogs.Add(new AuditLog
        {
            ActorId = currentUserId,
            Action = "identity.unlinked",
            TargetType = "identity_credential",
            TargetId = credential.Id,
            CreatedAt = DateTimeOffset.UtcNow,
            Details = JsonSerializer.SerializeToDocument(new
            {
                provider = credential.Provider.ToString(),
            }),
        });
        await db.SaveChangesAsync(cancellationToken);
        return new UnlinkOutcome.Removed();
    }

    private static string BuildMetadata(string? email, bool emailVerified, IDictionary<string, string?>? extras)
    {
        var dict = new Dictionary<string, object?>
        {
            ["email"] = email,
            ["email_verified"] = emailVerified,
        };
        if (extras is not null)
        {
            foreach (var (k, v) in extras) dict[k] = v;
        }
        return JsonSerializer.Serialize(dict);
    }

    // Suggest a starting handle for the onboarding picker from the email local-part. The
    // picker shows this pre-filled but lets the user edit; the final uniqueness check happens
    // at insert time.
    public static string? SuggestHandle(string? email)
    {
        if (string.IsNullOrWhiteSpace(email)) return null;
        var at = email.IndexOf('@');
        var local = at > 0 ? email[..at] : email;
        var cleaned = new string(local.Where(c => char.IsAsciiLetterOrDigit(c) || c is '_' or '-').ToArray());
        if (cleaned.Length < 3) return null;
        if (cleaned.Length > 32) cleaned = cleaned[..32];
        return cleaned;
    }
}
