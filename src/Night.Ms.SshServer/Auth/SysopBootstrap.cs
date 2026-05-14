using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Auth;

// Reads NIGHTMS_BOOTSTRAP_SYSOP_HANDLE at startup and ensures (a) any existing user with that
// handle has IsSysop = true, and (b) RegisterScreen sets IsSysop = true if a brand-new
// registration matches the handle. Useful for first-time deploys where you need an account
// that can self-promote others without manual SQL.
public sealed class SysopBootstrap(NightMsOptions options, IServiceProvider services, ILogger<SysopBootstrap> logger) : IHostedService
{
    public string? Handle { get; } = options.BootstrapSysopHandle;

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
            logger.LogInformation("Sysop handle '{Handle}' not yet registered — RegisterScreen will promote them on signup.", Handle);
            return;
        }
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
            await db.SaveChangesAsync(cancellationToken);
            logger.LogInformation("Promoted '{Handle}' to sysop via NIGHTMS_BOOTSTRAP_SYSOP_HANDLE.", Handle);
        }
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
}
