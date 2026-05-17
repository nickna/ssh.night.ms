using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Auth;

// Reads NIGHTMS_BOOTSTRAP_SYSOP_HANDLE (and optionally _PASSWORD) at startup. Behavior:
//   1. If only HANDLE is set: existing user with that handle gets IsSysop = true. No-op if
//      missing — RegisterScreen/IdentityResolutionService promote on signup via
//      IsBootstrapHandle.
//   2. If HANDLE + PASSWORD are both set AND user is missing: create the user with the
//      hashed password, IsSysop = true. This is the "fresh deploy with no web access"
//      escape hatch.
//   3. If HANDLE + PASSWORD are both set AND user exists with password_hash = null: seed
//      the hash so the sysop can log in over SSH with that password.
//   4. If user exists with password_hash already populated: NEVER overwrite. An admin who
//      changed their password via UI shouldn't be reset on container restart.
// Re-runs on every boot but is idempotent past the first successful seed.
public sealed class SysopBootstrap(
    NightMsOptions options,
    IServiceProvider services,
    IPasswordHasher hasher,
    ILogger<SysopBootstrap> logger) : IHostedService
{
    public string? Handle { get; } = options.BootstrapSysopHandle;
    private readonly string? _password = options.BootstrapSysopPassword;

    public bool IsBootstrapHandle(string handle) =>
        !string.IsNullOrEmpty(Handle) && string.Equals(handle, Handle, StringComparison.OrdinalIgnoreCase);

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(Handle))
        {
            return;
        }

        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var user = await db.Users.FirstOrDefaultAsync(u => u.Handle == Handle, cancellationToken);

        if (user is null)
        {
            if (string.IsNullOrEmpty(_password))
            {
                logger.LogInformation("Sysop handle '{Handle}' not yet registered — RegisterScreen will promote them on signup.", Handle);
                return;
            }

            // Bootstrap-with-password: stand the sysop account up from scratch so a fresh
            // deploy with no web SSO and no existing accounts can still get logged in.
            var hashed = hasher.Hash(_password);
            var now = DateTimeOffset.UtcNow;
            user = new User
            {
                Handle = Handle,
                CreatedAt = now,
                IsSysop = true,
                PasswordHash = hashed.Hash,
                PasswordAlgo = hashed.Algo,
                PasswordUpdatedAt = now,
            };
            db.Users.Add(user);
            db.AuditLogs.Add(new AuditLog
            {
                ActorId = null,
                Action = "sysop.bootstrap_seeded",
                TargetType = "user",
                CreatedAt = now,
                Details = JsonSerializer.SerializeToDocument(new { handle = Handle, with_password = true }),
            });
            await db.SaveChangesAsync(cancellationToken);
            logger.LogInformation("Bootstrapped sysop user '{Handle}' from env vars with seeded password.", Handle);
            return;
        }

        var changed = false;

        if (!user.IsSysop)
        {
            user.IsSysop = true;
            db.AuditLogs.Add(new AuditLog
            {
                ActorId = null,
                Action = "sysop.bootstrap",
                TargetType = "user",
                TargetId = user.Id,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            changed = true;
            logger.LogInformation("Promoted '{Handle}' to sysop via NIGHTMS_BOOTSTRAP_SYSOP_HANDLE.", Handle);
        }

        // Seed password ONLY if user has none. Never overwrite — would clobber an admin's
        // UI-changed password on every container restart.
        if (!string.IsNullOrEmpty(_password) && user.PasswordHash is null)
        {
            var hashed = hasher.Hash(_password);
            user.PasswordHash = hashed.Hash;
            user.PasswordAlgo = hashed.Algo;
            user.PasswordUpdatedAt = DateTimeOffset.UtcNow;
            db.AuditLogs.Add(new AuditLog
            {
                ActorId = null,
                Action = "sysop.bootstrap_seeded_password",
                TargetType = "user",
                TargetId = user.Id,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            changed = true;
            logger.LogInformation("Seeded initial password for sysop '{Handle}'.", Handle);
        }

        if (changed) await db.SaveChangesAsync(cancellationToken);
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
}
