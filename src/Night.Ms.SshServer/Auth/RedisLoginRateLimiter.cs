using System.Net;
using Night.Ms.SshServer.Configuration;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Auth;

public sealed class RedisLoginRateLimiter(IConnectionMultiplexer redis, NightMsOptions options, ILogger<RedisLoginRateLimiter> logger) : ILoginRateLimiter
{
    private readonly LoginLockoutOptions _opts = options.Lockout;

    public async Task<RateLimitCheck> CheckAsync(string handle, IPAddress? sourceIp, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var handleKey = LockoutKeyHandle(handle);
        var ipKey = sourceIp is null ? null : LockoutKeyIp(sourceIp);

        // Two parallel TTL lookups. Whichever is set wins; if both, the longer wait wins.
        var handleTtlTask = db.KeyTimeToLiveAsync(handleKey);
        var ipTtlTask = ipKey is null ? Task.FromResult<TimeSpan?>(null) : db.KeyTimeToLiveAsync(ipKey);
        await Task.WhenAll(handleTtlTask, ipTtlTask);

        TimeSpan? winning = null;
        string? scope = null;
        if (handleTtlTask.Result is { } h && (winning is null || h > winning)) { winning = h; scope = "handle"; }
        if (ipTtlTask.Result is { } i && (winning is null || i > winning)) { winning = i; scope = "ip"; }

        return winning is { } w ? new RateLimitCheck.LockedOut(w, scope ?? "unknown") : RateLimitCheck.Allowed.Instance;
    }

    public async Task RecordFailureAsync(string handle, IPAddress? sourceIp, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var window = TimeSpan.FromSeconds(_opts.WindowSeconds);
        var lockout = TimeSpan.FromSeconds(_opts.LockoutSeconds);

        var handleCount = await IncrementWithExpiryAsync(db, FailKeyHandle(handle), window);
        if (handleCount >= _opts.HandleFailureThreshold)
        {
            await db.StringSetAsync(LockoutKeyHandle(handle), handleCount.ToString(), lockout);
            logger.LogWarning("Locked out handle={Handle} after {Count} failures", handle, handleCount);
        }

        if (sourceIp is not null)
        {
            var ipCount = await IncrementWithExpiryAsync(db, FailKeyIp(sourceIp), window);
            if (ipCount >= _opts.IpFailureThreshold)
            {
                await db.StringSetAsync(LockoutKeyIp(sourceIp), ipCount.ToString(), lockout);
                logger.LogWarning("Locked out ip={Ip} after {Count} failures", sourceIp, ipCount);
            }
        }
    }

    public Task ClearAsync(string handle, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        // Clear the per-handle counter + active lockout. Don't touch IP scope — a single
        // successful login shouldn't whitewash a noisy source IP.
        return Task.WhenAll(
            db.KeyDeleteAsync(FailKeyHandle(handle)),
            db.KeyDeleteAsync(LockoutKeyHandle(handle)));
    }

    private static async Task<long> IncrementWithExpiryAsync(IDatabase db, string key, TimeSpan window)
    {
        // INCR then EXPIRE-if-new (NX flag) so we don't extend the window on every failure.
        var count = await db.StringIncrementAsync(key);
        if (count == 1)
        {
            await db.KeyExpireAsync(key, window);
        }
        return count;
    }

    private static string FailKeyHandle(string handle) => $"pwfail:handle:{handle.ToLowerInvariant()}";
    private static string FailKeyIp(IPAddress ip) => $"pwfail:ip:{ip}";
    private static string LockoutKeyHandle(string handle) => $"lockout:handle:{handle.ToLowerInvariant()}";
    private static string LockoutKeyIp(IPAddress ip) => $"lockout:ip:{ip}";
}
