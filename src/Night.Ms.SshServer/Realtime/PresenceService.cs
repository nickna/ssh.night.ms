using System.Text.Json;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Realtime;

// Per-channel "who's here right now" tracker, backed by a Redis sorted set keyed by
// last-heartbeat timestamp. Sessions call HeartbeatAsync every ~10s while attached to a
// channel; ListAsync evicts entries older than the staleness window before returning the
// snapshot, so a session that died without a clean Leave drops off the list within ~30s.
//
// We use a sorted set (ZADD score=now value=handle) so eviction is a single ZREMRANGEBYSCORE
// call on read — no scheduled cleanup task, no race between writers and a sweeper. The score
// is unix seconds; values are the raw handle for simple listing.
public sealed class PresenceService(IConnectionMultiplexer redis, IRealtimeBus bus)
{
    // How long a session's heartbeat is honored before they're considered gone. The session
    // heartbeats every 10s, so 30s gives two missed beats of grace before eviction.
    private static readonly TimeSpan StaleAfter = TimeSpan.FromSeconds(30);

    public async Task JoinAsync(long channelId, long userId, string handle, CancellationToken ct)
    {
        await HeartbeatInternalAsync(channelId, handle, ct);
        await PublishPresenceAsync(channelId, userId, handle, PresenceEventKind.Join, ct);
    }

    public async Task LeaveAsync(long channelId, long userId, string handle, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var key = Key(channelId);
        await db.SortedSetRemoveAsync(key, handle);
        await PublishPresenceAsync(channelId, userId, handle, PresenceEventKind.Leave, ct);
    }

    public async Task HeartbeatAsync(long channelId, long userId, string handle, CancellationToken ct)
    {
        await HeartbeatInternalAsync(channelId, handle, ct);
        // Heartbeats don't broadcast — they just keep the sorted-set entry alive. Other
        // sessions infer "still here" implicitly through ListAsync, not via fanout (which
        // would be noisy: N sessions × 6 heartbeats/min = N×6 envelopes/min/channel of pure
        // no-op signal).
    }

    // Transient "X is typing…" ping. Doesn't touch Redis state — typing is ephemeral and
    // each subscriber decides locally how long to keep the hint visible (we default to ~4s
    // on the receiving side). Caller is responsible for debouncing so a 10wpm typer doesn't
    // fan out 600 events per minute.
    public async Task PublishTypingAsync(long channelId, long userId, string handle, CancellationToken ct)
    {
        await PublishPresenceAsync(channelId, userId, handle, PresenceEventKind.Typing, ct);
    }

    public async Task<IReadOnlyList<string>> ListAsync(long channelId, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var key = Key(channelId);
        var cutoff = NowUnix() - (long)StaleAfter.TotalSeconds;
        // ZREMRANGEBYSCORE evicts stale entries inline. Doing it here (on read) means the
        // last reader of a half-empty channel cleans up after departed sessions; doing it
        // on heartbeat would be redundant work N times per minute per session.
        await db.SortedSetRemoveRangeByScoreAsync(key, double.NegativeInfinity, cutoff);
        var values = await db.SortedSetRangeByRankAsync(key, 0, -1);
        return values.Select(v => v.ToString()).Where(s => !string.IsNullOrEmpty(s)).ToArray();
    }

    private async Task HeartbeatInternalAsync(long channelId, string handle, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var key = Key(channelId);
        // ZADD with current unix time as score. CommandFlags.FireAndForget would be cheaper
        // but we want the await to surface Redis transport errors at the call site instead
        // of swallowing them silently.
        await db.SortedSetAddAsync(key, handle, NowUnix());
    }

    private async Task PublishPresenceAsync(long channelId, long userId, string handle, string kind, CancellationToken ct)
    {
        var dto = new PresenceEventDto(channelId, userId, handle, kind, DateTimeOffset.UtcNow);
        var bytes = JsonSerializer.SerializeToUtf8Bytes(dto);
        await bus.PublishAsync(ChatTopics.Presence(channelId), bytes, ct);
    }

    private static double NowUnix() => DateTimeOffset.UtcNow.ToUnixTimeSeconds();
    private static string Key(long channelId) => $"presence:channel:{channelId}";
}
