using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Realtime;

// Channel discovery + access for the chat UI. Each public method opens a fresh DbContext
// from the factory so the service is safe to share as a singleton across SSH session
// threads without sharing a tracked context.
public sealed class ChatService(IDbContextFactory<AppDbContext> dbFactory)
{
    public abstract record JoinResult
    {
        public sealed record Joined(Channel Channel) : JoinResult;
        public sealed record Created(Channel Channel) : JoinResult;
        public sealed record Denied(string Reason) : JoinResult;
        public sealed record InvalidName(string Reason) : JoinResult;
        public sealed record UserNotFound(string Handle) : JoinResult;
    }

    // Public channels: '#' is stripped, name normalized to lowercase. Auto-created on first
    // join (BBS-style) so anyone can spin up #random or #help by typing the command.
    public async Task<JoinResult> JoinPublicChannelAsync(string rawName, long actorUserId, CancellationToken ct)
    {
        var name = NormalizeChannelName(rawName);
        if (!IsValidPublicName(name, out var nameError))
        {
            return new JoinResult.InvalidName(nameError);
        }

        await using var db = await dbFactory.CreateDbContextAsync(ct);

        var existing = await db.Channels.FirstOrDefaultAsync(c => c.Name == name, ct);
        if (existing is not null)
        {
            if (existing.IsPrivate)
            {
                return new JoinResult.Denied($"#{name} is a private channel.");
            }
            return new JoinResult.Joined(existing);
        }

        var channel = new Channel
        {
            Name = name,
            Topic = null,
            IsPrivate = false,
            CreatedById = actorUserId,
            CreatedAt = DateTimeOffset.UtcNow,
        };
        db.Channels.Add(channel);
        try
        {
            await db.SaveChangesAsync(ct);
            return new JoinResult.Created(channel);
        }
        catch (DbUpdateException)
        {
            // Two clients typed /join #foo at the same time — the unique index on Channel.Name
            // will reject the second insert. Re-look up the now-existing row.
            db.ChangeTracker.Clear();
            var winner = await db.Channels.FirstOrDefaultAsync(c => c.Name == name, ct);
            if (winner is null) throw;
            return winner.IsPrivate
                ? new JoinResult.Denied($"#{name} is a private channel.")
                : new JoinResult.Joined(winner);
        }
    }

    // DMs are private channels with a deterministic name: dm-{minHandle}-{maxHandle}, so the
    // same pair always resolves to the same channel regardless of who initiated. Exactly two
    // ChannelMember rows are created on first DM.
    public async Task<JoinResult> JoinDmAsync(User actor, string otherHandle, CancellationToken ct)
    {
        otherHandle = otherHandle.Trim();
        if (string.IsNullOrEmpty(otherHandle))
        {
            return new JoinResult.InvalidName("/dm requires a handle.");
        }
        if (string.Equals(otherHandle, actor.Handle, StringComparison.OrdinalIgnoreCase))
        {
            return new JoinResult.Denied("You can't DM yourself.");
        }

        await using var db = await dbFactory.CreateDbContextAsync(ct);

        var other = await db.Users.FirstOrDefaultAsync(u => u.Handle == otherHandle, ct);
        if (other is null)
        {
            return new JoinResult.UserNotFound(otherHandle);
        }
        if (other.IsBanned)
        {
            return new JoinResult.Denied($"{other.Handle} is banned.");
        }

        var dmName = DmChannelName(actor.Handle, other.Handle);
        var existing = await db.Channels.FirstOrDefaultAsync(c => c.Name == dmName, ct);
        if (existing is not null)
        {
            return new JoinResult.Joined(existing);
        }

        var channel = new Channel
        {
            Name = dmName,
            Topic = $"DM between {actor.Handle} and {other.Handle}",
            IsPrivate = true,
            CreatedById = actor.Id,
            CreatedAt = DateTimeOffset.UtcNow,
        };
        db.Channels.Add(channel);
        try
        {
            await db.SaveChangesAsync(ct);
        }
        catch (DbUpdateException)
        {
            // Both pair members typed /dm at the same time — the unique index on Channel.Name
            // rejected the second insert. Defer to the winner; their members are being added
            // on the other path.
            db.ChangeTracker.Clear();
            var winner = await db.Channels.FirstOrDefaultAsync(c => c.Name == dmName, ct);
            if (winner is null) throw;
            return new JoinResult.Joined(winner);
        }

        var now = DateTimeOffset.UtcNow;
        db.ChannelMembers.Add(new ChannelMember { ChannelId = channel.Id, UserId = actor.Id, JoinedAt = now, Role = "member" });
        db.ChannelMembers.Add(new ChannelMember { ChannelId = channel.Id, UserId = other.Id, JoinedAt = now, Role = "member" });
        await db.SaveChangesAsync(ct);

        return new JoinResult.Created(channel);
    }

    public async Task<bool> CanAccessAsync(long channelId, long userId, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var channel = await db.Channels.AsNoTracking().FirstOrDefaultAsync(c => c.Id == channelId, ct);
        if (channel is null) return false;
        if (!channel.IsPrivate) return true;
        return await db.ChannelMembers.AnyAsync(m => m.ChannelId == channelId && m.UserId == userId, ct);
    }

    // Deterministic DM name: lowercase + alphabetical ordering of the two handles.
    public static string DmChannelName(string handleA, string handleB)
    {
        var a = handleA.ToLowerInvariant();
        var b = handleB.ToLowerInvariant();
        var (lo, hi) = string.CompareOrdinal(a, b) < 0 ? (a, b) : (b, a);
        return $"dm-{lo}-{hi}";
    }

    private static string NormalizeChannelName(string raw) =>
        (raw ?? string.Empty).TrimStart('#').Trim().ToLowerInvariant();

    private static bool IsValidPublicName(string name, out string error)
    {
        if (name.Length is < 2 or > 32)
        {
            error = "Channel name must be 2–32 chars.";
            return false;
        }
        if (name.StartsWith("dm-", StringComparison.Ordinal))
        {
            // dm- prefix is reserved for direct messages; you can only get into a DM via /dm.
            error = "The 'dm-' prefix is reserved.";
            return false;
        }
        foreach (var c in name)
        {
            if (!(char.IsAsciiLetterOrDigit(c) || c is '_' or '-'))
            {
                error = "Channel names allow letters, digits, underscore, and dash only.";
                return false;
            }
        }
        error = string.Empty;
        return true;
    }
}
