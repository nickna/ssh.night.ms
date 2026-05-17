using Night.Ms.SshServer.Configuration;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Auth;

public sealed class RedisDismissedKeyStore(IConnectionMultiplexer redis, NightMsOptions options) : IDismissedKeyStore
{
    private readonly TimeSpan _ttl = TimeSpan.FromDays(options.KeyAdoption.DismissalTtlDays);

    public async Task<bool> IsDismissedAsync(long userId, string fingerprint, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        return await db.KeyExistsAsync(Key(userId, fingerprint));
    }

    public Task DismissAsync(long userId, string fingerprint, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        return db.StringSetAsync(Key(userId, fingerprint), "1", _ttl);
    }

    private static string Key(long userId, string fingerprint) => $"dismissed:{userId}:{fingerprint}";
}
