using System.Collections.Concurrent;

namespace Night.Ms.SshServer.Caching;

// Per-key TTL cache for values that callers re-fetch from upstream APIs. Replaces the
// hand-rolled lock/ConcurrentDictionary + DateTimeOffset bookkeeping that HackerNewsProvider
// and OpenMeteoWeatherProvider each grew independently.
//
// Two read paths:
//   - TryGetFresh: returns a cached entry only if it hasn't passed its TTL. Hot path for
//     the "do I need to refetch?" check.
//   - TryGetAny: returns a cached entry regardless of age. Callers use this on the failure
//     path to serve a stale snapshot when the upstream call throws.
//
// The cache stores the latest successful result per key. There is no single-flight — two
// concurrent callers may both fetch, and whichever finishes last wins. That matches the
// existing per-provider behavior; providers in this codebase aren't on a hot enough path
// to need request coalescing.
internal sealed class TtlAsyncCache<TKey, TValue>(TimeSpan ttl)
    where TKey : notnull
{
    private readonly ConcurrentDictionary<TKey, Entry> _cache = new();

    public bool TryGetFresh(TKey key, out TValue value)
    {
        if (_cache.TryGetValue(key, out var entry) && DateTimeOffset.UtcNow - entry.FetchedAt < ttl)
        {
            value = entry.Value;
            return true;
        }
        value = default!;
        return false;
    }

    public bool TryGetAny(TKey key, out TValue value)
    {
        if (_cache.TryGetValue(key, out var entry))
        {
            value = entry.Value;
            return true;
        }
        value = default!;
        return false;
    }

    public void Set(TKey key, TValue value)
    {
        _cache[key] = new Entry(value, DateTimeOffset.UtcNow);
    }

    private sealed record Entry(TValue Value, DateTimeOffset FetchedAt);
}
