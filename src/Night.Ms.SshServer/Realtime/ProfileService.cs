using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Realtime;

public sealed record ProfileSnapshot(
    long UserId,
    string Handle,
    string? RealName,
    string? Location,
    string? Bio,
    DateTimeOffset CreatedAt,
    DateTimeOffset? LastSeenAt,
    bool IsSysop,
    int ChatMessageCount,
    int TopicCount,
    int PostCount);

public sealed record ProfileUpdateResult(bool Ok, string? Error);

public sealed class ProfileService(IServiceProvider services)
{
    public const int MaxBioLength = 500;
    public const int MaxLocationLength = 64;
    public const int MaxRealNameLength = 64;

    // Looks up a profile by handle, including derived counts. Returns null when the user
    // doesn't exist (the caller decides how to render that — /finger prints "no such user").
    public async Task<ProfileSnapshot?> GetByHandleAsync(string handle, CancellationToken ct)
    {
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();

        var user = await db.Users.AsNoTracking().FirstOrDefaultAsync(u => u.Handle == handle, ct);
        if (user is null) return null;

        var chatCount = await db.ChatMessages.CountAsync(m => m.UserId == user.Id, ct);
        var topicCount = await db.Topics.CountAsync(t => t.CreatedById == user.Id, ct);
        var postCount = await db.Posts.CountAsync(p => p.CreatedById == user.Id, ct);

        return new ProfileSnapshot(
            UserId: user.Id,
            Handle: user.Handle,
            RealName: user.RealName,
            Location: user.Location,
            Bio: user.Bio,
            CreatedAt: user.CreatedAt,
            LastSeenAt: user.LastSeenAt,
            IsSysop: user.IsSysop,
            ChatMessageCount: chatCount,
            TopicCount: topicCount,
            PostCount: postCount);
    }

    public async Task<ProfileUpdateResult> UpdateAsync(long userId, string? realName, string? location, string? bio, CancellationToken ct)
    {
        realName = Trim(realName);
        location = Trim(location);
        bio = Trim(bio);

        if (realName?.Length > MaxRealNameLength)
            return new(false, $"Real name must be ≤ {MaxRealNameLength} characters.");
        if (location?.Length > MaxLocationLength)
            return new(false, $"Location must be ≤ {MaxLocationLength} characters.");
        if (bio?.Length > MaxBioLength)
            return new(false, $"Bio must be ≤ {MaxBioLength} characters.");

        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId, ct);
        if (user is null)
            return new(false, "User not found.");

        user.RealName = string.IsNullOrEmpty(realName) ? null : realName;
        user.Location = string.IsNullOrEmpty(location) ? null : location;
        user.Bio = string.IsNullOrEmpty(bio) ? null : bio;
        await db.SaveChangesAsync(ct);
        return new(true, null);
    }

    // Renders the snapshot as the multi-line text /finger prints into the chat log.
    public static string FormatFinger(ProfileSnapshot p)
    {
        var sb = new System.Text.StringBuilder();
        sb.Append("── finger ").Append(p.Handle).Append(p.IsSysop ? " (sysop) ──\n" : " ──\n");
        sb.Append("   joined     ").AppendLine(p.CreatedAt.ToLocalTime().ToString("yyyy-MM-dd"));
        sb.Append("   last seen  ").AppendLine(p.LastSeenAt is { } ls
            ? ls.ToLocalTime().ToString("yyyy-MM-dd HH:mm")
            : "<never>");
        if (!string.IsNullOrEmpty(p.RealName))
            sb.Append("   real name  ").AppendLine(p.RealName);
        if (!string.IsNullOrEmpty(p.Location))
            sb.Append("   location   ").AppendLine(p.Location);
        if (!string.IsNullOrEmpty(p.Bio))
            sb.Append("   bio        ").AppendLine(p.Bio);
        sb.Append("   stats      ")
          .Append(p.ChatMessageCount).Append(" chat / ")
          .Append(p.TopicCount).Append(" topics / ")
          .Append(p.PostCount).AppendLine(" posts");
        return sb.ToString();
    }

    private static string? Trim(string? s) => s?.Trim();
}
