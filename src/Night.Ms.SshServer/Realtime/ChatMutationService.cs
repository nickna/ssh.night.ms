using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Realtime;

// Edits / deletes / reactions. Carved out from ChatService so the "discovery + access" code
// stays small and this can hold the more complex authorization rules. Same per-call scope
// pattern: each public method opens its own DI scope to avoid sharing AppDbContext across
// SSH session threads.
public sealed class ChatMutationService(IServiceProvider services)
{
    public abstract record Result
    {
        public sealed record Ok : Result;
        public sealed record NotFound : Result;
        public sealed record Forbidden(string Reason) : Result;
        public sealed record Invalid(string Reason) : Result;

        public static readonly Ok OkInstance = new();
        public static readonly NotFound NotFoundInstance = new();
    }

    // Edit the body of an existing message. Author-only — sysop override is intentionally
    // not modeled here yet; the moderation surface is separate (audit_log + bans). A no-op
    // edit (body unchanged) returns Ok without writing to keep the audit signal clean.
    public async Task<Result> EditAsync(long messageId, long actorUserId, string newBody, CancellationToken ct)
    {
        if (string.IsNullOrWhiteSpace(newBody)) return new Result.Invalid("New body is empty.");
        if (newBody.Length > 2000)              return new Result.Invalid("Message too long.");

        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var msg = await db.ChatMessages.FirstOrDefaultAsync(m => m.Id == messageId, ct);
        if (msg is null) return Result.NotFoundInstance;
        if (msg.UserId != actorUserId) return new Result.Forbidden("You can only edit your own messages.");
        if (msg.DeletedAt is not null) return new Result.Forbidden("This message was deleted.");
        if (msg.Body == newBody) return Result.OkInstance;

        var editedAt = DateTimeOffset.UtcNow;
        msg.Body = newBody;
        msg.EditedAt = editedAt;
        await db.SaveChangesAsync(ct);

        var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
        await PublishEnvelopeAsync(bus, msg.ChannelId, ChatEventKind.Edit,
            new ChatEditDto(msg.Id, msg.ChannelId, msg.Body, editedAt), ct);
        return Result.OkInstance;
    }

    // Tombstone a message. The row remains so reactions and reply-chains don't dangle; the
    // renderer paints "(deleted)" in faint gray.
    public async Task<Result> DeleteAsync(long messageId, long actorUserId, CancellationToken ct)
    {
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var msg = await db.ChatMessages.FirstOrDefaultAsync(m => m.Id == messageId, ct);
        if (msg is null) return Result.NotFoundInstance;
        if (msg.UserId != actorUserId) return new Result.Forbidden("You can only delete your own messages.");
        if (msg.DeletedAt is not null) return Result.OkInstance;

        msg.DeletedAt = DateTimeOffset.UtcNow;
        await db.SaveChangesAsync(ct);

        var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
        await PublishEnvelopeAsync(bus, msg.ChannelId, ChatEventKind.Delete,
            new ChatDeleteDto(msg.Id, msg.ChannelId), ct);
        return Result.OkInstance;
    }

    // Toggle-style reaction add. Re-adding the same emoji by the same user is a no-op
    // (composite PK enforces uniqueness; the catch covers the rare race). Emoji must be
    // one of our known glyphs — silently ignoring unknown shortcodes here would let users
    // spam the wire with arbitrary text, so we require the caller to look it up first.
    public async Task<Result> ReactAsync(long messageId, long actorUserId, string actorHandle, string emoji, CancellationToken ct)
    {
        if (string.IsNullOrEmpty(emoji)) return new Result.Invalid("Empty emoji.");
        if (emoji.Length > 32)            return new Result.Invalid("Emoji too long.");

        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var msg = await db.ChatMessages.AsNoTracking().FirstOrDefaultAsync(m => m.Id == messageId, ct);
        if (msg is null) return Result.NotFoundInstance;
        if (msg.DeletedAt is not null) return new Result.Forbidden("This message was deleted.");

        var now = DateTimeOffset.UtcNow;
        try
        {
            db.MessageReactions.Add(new MessageReaction
            {
                MessageId = messageId,
                UserId = actorUserId,
                Emoji = emoji,
                CreatedAt = now,
            });
            await db.SaveChangesAsync(ct);
        }
        catch (DbUpdateException)
        {
            // Already exists — duplicate add is a benign no-op.
            return Result.OkInstance;
        }

        var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
        await PublishEnvelopeAsync(bus, msg.ChannelId, ChatEventKind.React,
            new ChatReactionDto(messageId, msg.ChannelId, actorUserId, actorHandle, emoji), ct);
        return Result.OkInstance;
    }

    public async Task<Result> UnreactAsync(long messageId, long actorUserId, string actorHandle, string emoji, CancellationToken ct)
    {
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var row = await db.MessageReactions.FirstOrDefaultAsync(
            r => r.MessageId == messageId && r.UserId == actorUserId && r.Emoji == emoji, ct);
        if (row is null) return Result.NotFoundInstance;

        var msg = await db.ChatMessages.AsNoTracking().FirstOrDefaultAsync(m => m.Id == messageId, ct);
        if (msg is null) return Result.NotFoundInstance;

        db.MessageReactions.Remove(row);
        await db.SaveChangesAsync(ct);

        var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
        await PublishEnvelopeAsync(bus, msg.ChannelId, ChatEventKind.Unreact,
            new ChatReactionDto(messageId, msg.ChannelId, actorUserId, actorHandle, emoji), ct);
        return Result.OkInstance;
    }

    // Pin a message in its channel. The recommendation flagged "ops only" but private-channel
    // role enforcement is a follow-up; for now pinning is open to any participant — it's
    // reversible and non-destructive. Idempotent: pinning an already-pinned message is Ok.
    public async Task<Result> PinAsync(long messageId, long actorUserId, CancellationToken ct)
    {
        return await SetPinAsync(messageId, actorUserId, pin: true, ct);
    }

    public async Task<Result> UnpinAsync(long messageId, long actorUserId, CancellationToken ct)
    {
        return await SetPinAsync(messageId, actorUserId, pin: false, ct);
    }

    private async Task<Result> SetPinAsync(long messageId, long actorUserId, bool pin, CancellationToken ct)
    {
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var msg = await db.ChatMessages.FirstOrDefaultAsync(m => m.Id == messageId, ct);
        if (msg is null) return Result.NotFoundInstance;
        if (msg.DeletedAt is not null) return new Result.Forbidden("Can't pin a deleted message.");
        if (msg.IsPinned == pin) return Result.OkInstance;

        msg.IsPinned = pin;
        await db.SaveChangesAsync(ct);

        var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
        await PublishEnvelopeAsync(bus, msg.ChannelId, pin ? ChatEventKind.Pin : ChatEventKind.Unpin,
            new ChatPinDto(msg.Id, msg.ChannelId, pin), ct);
        return Result.OkInstance;
    }

    // List currently-pinned messages for a channel. Capped at 50 — pinning is for "the few
    // things worth keeping," not an unbounded backlog. Ordered newest-pinned-first.
    public async Task<IReadOnlyList<ChatMessage>> ListPinsAsync(long channelId, CancellationToken ct)
    {
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        return await db.ChatMessages
            .AsNoTracking()
            .Where(m => m.ChannelId == channelId && m.IsPinned && m.DeletedAt == null)
            .OrderByDescending(m => m.CreatedAt)
            .Take(50)
            .Include(m => m.User)
            .ToListAsync(ct);
    }

    // Set the channel topic. Only the channel creator may change the topic; a public channel
    // with a NULL CreatedById (set on rare admin-seeded rows) falls back to any-member.
    // Passing a null/whitespace topic clears it.
    public async Task<Result> SetTopicAsync(long channelId, long actorUserId, string actorHandle, string? topic, CancellationToken ct)
    {
        if (topic is not null && topic.Length > 200) return new Result.Invalid("Topic must be 200 chars or fewer.");
        var trimmed = string.IsNullOrWhiteSpace(topic) ? null : topic.Trim();

        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var channel = await db.Channels.FirstOrDefaultAsync(c => c.Id == channelId, ct);
        if (channel is null) return Result.NotFoundInstance;
        if (channel.CreatedById is { } creatorId && creatorId != actorUserId)
        {
            return new Result.Forbidden("Only the channel creator can set the topic.");
        }
        if (channel.Topic == trimmed) return Result.OkInstance;

        channel.Topic = trimmed;
        await db.SaveChangesAsync(ct);

        var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
        await PublishEnvelopeAsync(bus, channelId, ChatEventKind.Topic,
            new ChatTopicDto(channelId, trimmed, actorUserId, actorHandle), ct);
        return Result.OkInstance;
    }

    // Search the recent history of a channel for messages whose body matches a term. Uses
    // Postgres full-text search (websearch_to_tsquery + the generated body_search column)
    // when the term parses to a valid tsquery; falls back to case-insensitive ILIKE for
    // single-token queries that don't tokenize cleanly. Excludes deleted messages.
    //
    // websearch_to_tsquery accepts Google-style input ("foo bar", "exact phrase", -negate)
    // and never throws on malformed input, so we don't need a try/catch fallback for
    // syntax errors — but a very-short or all-punctuation term still yields an empty
    // tsquery, which would match nothing. The ILIKE fallback handles those cases.
    public async Task<IReadOnlyList<ChatMessage>> SearchAsync(long channelId, string term, int limit, CancellationToken ct)
    {
        if (string.IsNullOrWhiteSpace(term)) return Array.Empty<ChatMessage>();
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var trimmed = term.Trim();
        var clamped = Math.Clamp(limit, 1, 100);

        // FTS path: hits the GIN index on body_search. EF.Functions.WebSearchToTsQuery +
        // .Matches() emit `body_search @@ websearch_to_tsquery('simple', term)`.
        var ftsHits = await db.ChatMessages
            .FromSqlInterpolated(
                $@"SELECT * FROM chat_messages
                   WHERE channel_id = {channelId}
                     AND deleted_at IS NULL
                     AND body_search @@ websearch_to_tsquery('english', {trimmed})
                   ORDER BY created_at DESC
                   LIMIT {clamped}")
            .AsNoTracking()
            .Include(m => m.User)
            .ToListAsync(ct);

        if (ftsHits.Count > 0) return ftsHits;

        // Fallback: short token or punctuation-only term that tsquery couldn't tokenize.
        // Same |-escaped ILIKE pattern we used before the FTS upgrade.
        const string esc = "|";
        var pattern = "%" + trimmed
            .Replace("|", "||")
            .Replace("%", "|%")
            .Replace("_", "|_") + "%";
        return await db.ChatMessages
            .AsNoTracking()
            .Where(m => m.ChannelId == channelId && m.DeletedAt == null && EF.Functions.ILike(m.Body, pattern, esc))
            .OrderByDescending(m => m.CreatedAt)
            .Take(clamped)
            .Include(m => m.User)
            .ToListAsync(ct);
    }

    // For a set of parent ids, count children grouped by parent. Used to hydrate the
    // "[N replies]" badge on channel history load.
    public async Task<IReadOnlyDictionary<long, int>> SnapshotReplyCountsAsync(
        IReadOnlyCollection<long> parentIds, CancellationToken ct)
    {
        if (parentIds.Count == 0) return new Dictionary<long, int>();
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var rows = await db.ChatMessages
            .AsNoTracking()
            .Where(m => m.ParentMessageId != null && parentIds.Contains(m.ParentMessageId.Value) && m.DeletedAt == null)
            .GroupBy(m => m.ParentMessageId!.Value)
            .Select(g => new { ParentId = g.Key, Count = g.Count() })
            .ToListAsync(ct);
        return rows.ToDictionary(r => r.ParentId, r => r.Count);
    }

    // Snapshot of reactions for a set of messages — used when loading channel history so the
    // initial render shows the current totals instead of waiting for live events to redraw.
    public async Task<IReadOnlyDictionary<long, List<MessageReaction>>> SnapshotReactionsAsync(
        IReadOnlyCollection<long> messageIds, CancellationToken ct)
    {
        if (messageIds.Count == 0) return new Dictionary<long, List<MessageReaction>>();
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var rows = await db.MessageReactions
            .Where(r => messageIds.Contains(r.MessageId))
            .ToListAsync(ct);
        return rows.GroupBy(r => r.MessageId).ToDictionary(g => g.Key, g => g.ToList());
    }

    private static async Task PublishEnvelopeAsync<T>(IRealtimeBus bus, long channelId, string kind, T payload, CancellationToken ct)
    {
        var envelope = new ChatEnvelope(kind, JsonSerializer.SerializeToElement(payload));
        var bytes = JsonSerializer.SerializeToUtf8Bytes(envelope);
        await bus.PublishAsync(ChatTopics.Channel(channelId), bytes, ct);
    }
}
