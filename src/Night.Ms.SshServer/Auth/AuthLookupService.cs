using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Auth;

public sealed class AuthLookupService(IDbContextFactory<AppDbContext> dbFactory, ILogger<AuthLookupService> logger)
{
    public async Task<AuthDecision> LookupAsync(AuthQuery query, CancellationToken cancellationToken)
    {
        await using var db = await dbFactory.CreateDbContextAsync(cancellationToken);

        var key = await db.SshKeys
            .Include(k => k.User)
            .AsNoTracking()
            .FirstOrDefaultAsync(k => k.Fingerprint == query.Fingerprint, cancellationToken);

        if (key is null)
        {
            return AuthDecision.Unknown.Instance;
        }

        var user = key.User!;
        if (user.IsBanned)
        {
            logger.LogWarning("Banned account attempted login: handle={Handle} fingerprint={Fingerprint}",
                user.Handle, query.Fingerprint);
            return new AuthDecision.Banned("Account is banned.");
        }

        return new AuthDecision.Known(user.Id, user.Handle, user.IsSysop);
    }
}
