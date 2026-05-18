using System.Security.Cryptography;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Auth;

public sealed class RedisBridgeTokenStore(IConnectionMultiplexer redis) : IBridgeTokenStore
{
    // 5 minutes is short enough that a leaked URL has a narrow exposure window but long
    // enough that a user who alt-tabs to copy the link into a browser still makes it.
    private static readonly TimeSpan Ttl = TimeSpan.FromMinutes(5);

    // Sliding-window-ish per-user issuance cap. INCR a key with a 1-hour TTL on first hit;
    // the count tracks roughly the last hour of issuance attempts. 5 is enough for a real
    // user to click twice and retry a couple of times; high enough to not get in the way,
    // low enough to throttle a compromised session generating an unlimited stream.
    private const int MaxPerHour = 5;
    private static readonly TimeSpan RateWindow = TimeSpan.FromHours(1);

    public async Task<string?> IssueAsync(long userId, CancellationToken ct)
    {
        var db = redis.GetDatabase();

        // Atomic per-user counter. INCR returns the post-increment value; on first hit
        // (count == 1) install the 1-hour TTL so subsequent calls within the window see it.
        var rateKey = $"bridge:rate:{userId}";
        var count = await db.StringIncrementAsync(rateKey);
        if (count == 1)
        {
            await db.KeyExpireAsync(rateKey, RateWindow);
        }
        if (count > MaxPerHour)
        {
            // Don't decrement on refusal — we want the cap to be a hard ceiling per window.
            return null;
        }

        // 32 random bytes → 43 chars of URL-safe base64. ~256 bits of entropy.
        var raw = RandomNumberGenerator.GetBytes(32);
        var token = ToUrlSafeBase64(raw);

        // Store SHA256(token) so a Redis dump doesn't yield working tokens. Value is the
        // user id the URL grants access to.
        await db.StringSetAsync(HashedKey(token), userId, Ttl);
        return token;
    }

    public async Task<long?> RedeemAsync(string token, CancellationToken ct)
    {
        if (string.IsNullOrEmpty(token)) return null;
        var db = redis.GetDatabase();
        // GETDEL: atomic read + delete. A token cannot be redeemed twice even if two
        // concurrent requests arrive (one wins the GET, the other sees an empty value).
        var value = await db.StringGetDeleteAsync(HashedKey(token));
        if (value.IsNullOrEmpty) return null;
        return long.TryParse(value.ToString(), out var userId) ? userId : null;
    }

    private static string HashedKey(string token)
    {
        var hash = SHA256.HashData(System.Text.Encoding.ASCII.GetBytes(token));
        return $"bridge:{Convert.ToHexStringLower(hash)}";
    }

    private static string ToUrlSafeBase64(byte[] bytes)
    {
        // Standard base64 with +/= replaced — RFC 4648 §5. URL-safe so the token can be a
        // path segment without encoding (and so a user copy/pasting the URL doesn't see %2B).
        return Convert.ToBase64String(bytes)
            .TrimEnd('=')
            .Replace('+', '-')
            .Replace('/', '_');
    }
}
