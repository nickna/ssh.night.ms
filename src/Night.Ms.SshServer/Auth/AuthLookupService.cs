using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Auth;

public sealed class AuthLookupService(IDbContextFactory<AppDbContext> dbFactory, ILogger<AuthLookupService> logger)
{
    public async Task<AuthDecision> LookupAsync(AuthQuery query, CancellationToken cancellationToken)
    {
        await using var db = await dbFactory.CreateDbContextAsync(cancellationToken);

        // SSH credentials live in the unified identity_credentials table alongside SSO
        // identities; the (Provider, Subject) pair is unique so this is a single index hit.
        var credential = await db.IdentityCredentials
            .Include(c => c.User)
            .AsNoTracking()
            .FirstOrDefaultAsync(
                c => c.Provider == CredentialProvider.Ssh && c.Subject == query.Fingerprint,
                cancellationToken);

        if (credential is null)
        {
            return AuthDecision.Unknown.Instance;
        }

        var user = credential.User!;
        if (user.IsBanned)
        {
            logger.LogWarning("Banned account attempted login: handle={Handle} fingerprint={Fingerprint}",
                user.Handle, query.Fingerprint);
            return new AuthDecision.Banned("Account is banned.");
        }

        return new AuthDecision.Known(user.Id, user.Handle, user.IsSysop);
    }
}
