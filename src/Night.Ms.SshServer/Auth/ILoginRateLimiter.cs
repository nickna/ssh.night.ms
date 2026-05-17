using System.Net;

namespace Night.Ms.SshServer.Auth;

// Sliding-window password-auth lockout. Two independent counters per attempt:
//   pwfail:handle:{lower(handle)} — protects an account from being brute-forced
//   pwfail:ip:{ip}                — protects against a single attacker hitting many handles
// Each counter expires after WindowSeconds, so accumulated failures naturally age out. When
// either threshold trips, a separate lockout key is set with TTL = LockoutSeconds. CheckAsync
// reports Allowed unless one of those keys exists.
//
// Successful logins clear the per-handle counter (via ClearAsync) so a returning user isn't
// punished for old typos; the IP counter is intentionally NOT cleared by a single success
// since a script can hit many handles from one IP and have one of them be correct.
public interface ILoginRateLimiter
{
    Task<RateLimitCheck> CheckAsync(string handle, IPAddress? sourceIp, CancellationToken ct);
    Task RecordFailureAsync(string handle, IPAddress? sourceIp, CancellationToken ct);
    Task ClearAsync(string handle, CancellationToken ct);
}

public abstract record RateLimitCheck
{
    public sealed record Allowed : RateLimitCheck { public static readonly Allowed Instance = new(); }
    public sealed record LockedOut(TimeSpan RetryAfter, string Scope) : RateLimitCheck;
}
