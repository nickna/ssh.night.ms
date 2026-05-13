using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Realtime;

// Per-user channel-read pointers, powering "channels I've touched" + unread badges.
// Each public method spins up a fresh DbContext via the factory so the singleton is safe
// across SSH session threads without sharing a tracked context.
public sealed class ReadStateService(IDbContextFactory<AppDbContext> dbFactory)
{
    public sealed record ChannelEntry(long ChannelId, string Name, string? Topic, bool IsPrivate, DateTimeOffset LastActivityAt, int UnreadCount);

    // Bump the read pointer for (user, channel) to the given message id, if it's newer than
    // what we have. Idempotent — re-marking with the same id is a touch on UpdatedAt only,
    // which keeps the channel sticky at the top of the sidebar.
    public async Task MarkReadAsync(long userId, long channelId, long lastReadMessageId, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var now = DateTimeOffset.UtcNow;
        var row = await db.ChannelReads
            .FirstOrDefaultAsync(r => r.UserId == userId && r.ChannelId == channelId, ct);
        if (row is null)
        {
            db.ChannelReads.Add(new ChannelRead
            {
                UserId = userId,
                ChannelId = channelId,
                LastReadMessageId = lastReadMessageId,
                UpdatedAt = now,
            });
        }
        else
        {
            // Never regress the pointer — if a slower handler tries to mark an older
            // message id, keep what we have. UpdatedAt still bumps so the channel stays
            // sticky at the top of the recent-activity sort.
            if (lastReadMessageId > row.LastReadMessageId)
            {
                row.LastReadMessageId = lastReadMessageId;
            }
            row.UpdatedAt = now;
        }
        await db.SaveChangesAsync(ct);
    }

    // Returns the channels this user has read state for, plus #lobby (the BBS-wide default
    // every session sees). Ordered by most-recent-activity in the channel: a channel with
    // newer messages than my pointer floats above ones I'm caught up on. Capped at 24 since
    // the sidebar can't show much more anyway at typical terminal heights.
    public async Task<IReadOnlyList<ChannelEntry>> ListForUserAsync(long userId, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);

        // Subquery: each channel's latest-message info (id + created-at) for unread math.
        // Done as one round-trip via GroupBy → Max to keep the per-refresh cost small.
        var reads = await db.ChannelReads
            .AsNoTracking()
            .Where(r => r.UserId == userId)
            .ToListAsync(ct);

        var subscribedIds = reads.Select(r => r.ChannelId).ToHashSet();

        // Always show #lobby even if the user hasn't been there yet. A missing #lobby row
        // here means the DatabaseInitializer hasn't seeded yet, which shouldn't happen at
        // runtime — but the LINQ still has to be safe against it.
        var lobby = await db.Channels.AsNoTracking().FirstOrDefaultAsync(c => c.Name == "lobby", ct);
        if (lobby is not null) subscribedIds.Add(lobby.Id);

        if (subscribedIds.Count == 0) return Array.Empty<ChannelEntry>();

        var ids = subscribedIds.ToArray();
        // Pull channel metadata.
        var channels = await db.Channels
            .AsNoTracking()
            .Where(c => ids.Contains(c.Id))
            .ToDictionaryAsync(c => c.Id, ct);

        // For each channel, find the most recent (still-undeleted) message + its id and
        // count of post-pointer messages.
        var latest = await db.ChatMessages
            .AsNoTracking()
            .Where(m => ids.Contains(m.ChannelId) && m.DeletedAt == null)
            .GroupBy(m => m.ChannelId)
            .Select(g => new
            {
                ChannelId = g.Key,
                LastMessageId = g.Max(x => x.Id),
                LastMessageAt = g.Max(x => x.CreatedAt),
            })
            .ToListAsync(ct);

        var readByChannel = reads.ToDictionary(r => r.ChannelId);

        // Unread count = messages with id > my pointer. Done one channel at a time so each
        // count is cheap (uses the (channel_id, created_at) descending index).
        var unread = new Dictionary<long, int>();
        foreach (var l in latest)
        {
            var pointer = readByChannel.TryGetValue(l.ChannelId, out var r) ? r.LastReadMessageId : 0;
            if (l.LastMessageId > pointer)
            {
                unread[l.ChannelId] = await db.ChatMessages.AsNoTracking()
                    .CountAsync(m => m.ChannelId == l.ChannelId && m.Id > pointer && m.DeletedAt == null, ct);
            }
        }

        // Sort: channels with unread first (newest activity first within that group), then
        // caught-up channels (also newest first). #lobby sticks to the top if it has zero
        // activity ever — gives the user a stable home row to return to.
        var entries = ids
            .Where(id => channels.ContainsKey(id))
            .Select(id =>
            {
                var c = channels[id];
                var latestRow = latest.FirstOrDefault(x => x.ChannelId == id);
                var lastAt = latestRow?.LastMessageAt
                          ?? readByChannel.GetValueOrDefault(id)?.UpdatedAt
                          ?? c.CreatedAt;
                var u = unread.GetValueOrDefault(id);
                return new ChannelEntry(c.Id, c.Name, c.Topic, c.IsPrivate, lastAt, u);
            })
            .OrderByDescending(e => e.UnreadCount > 0)
            .ThenByDescending(e => e.LastActivityAt)
            .Take(24)
            .ToList();

        return entries;
    }
}
