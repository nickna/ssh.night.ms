using System.Collections.Concurrent;

namespace Night.Ms.SshServer.Caching;

// In-process cache for async producers with a FIFO eviction policy. Two callers
// (HttpImageFetcher, OsmTileFetcher) used to hand-roll the same ConcurrentDictionary +
// Lazy + Queue + lock combo — that pattern lives here so a future caller (vector tiles,
// reverse-geocode results) just instantiates it.
//
// Eviction is best-effort: an evicted entry's value isn't disposed because in-flight
// callers may still hold the Task reference. The intent is purely memory pressure relief,
// not lifecycle management.
internal sealed class FifoAsyncCache<TKey, TValue>(int maxEntries)
    where TKey : notnull
{
    private readonly ConcurrentDictionary<TKey, Lazy<Task<TValue>>> _cache = new();
    private readonly Queue<TKey> _order = new();
    private readonly object _evictLock = new();

    // Returns the cached task for the key, creating it via factory on first request.
    // Subsequent callers within the lifetime of the entry await the same Task.
    public Task<TValue> GetOrAddAsync(TKey key, Func<TKey, Task<TValue>> factory, CancellationToken cancellationToken = default)
    {
        var entry = _cache.GetOrAdd(key, k => new Lazy<Task<TValue>>(
            () => factory(k),
            LazyThreadSafetyMode.ExecutionAndPublication));
        TrackInsert(key);
        return entry.Value.WaitAsync(cancellationToken);
    }

    private void TrackInsert(TKey key)
    {
        lock (_evictLock)
        {
            _order.Enqueue(key);
            while (_order.Count > maxEntries)
            {
                var oldest = _order.Dequeue();
                _cache.TryRemove(oldest, out _);
            }
        }
    }
}
