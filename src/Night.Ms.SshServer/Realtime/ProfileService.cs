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
    string TimeZoneId,
    int ChatMessageCount,
    int TopicCount,
    int PostCount);

public sealed record ProfileUpdateResult(bool Ok, string? Error);

// Bundles the editable profile fields so UpdateAsync doesn't sprout an 8-arg signature.
// Any string property left null clears the column; the display-preference fields are
// required because every user always has a concrete preference value.
public sealed record ProfileUpdate(
    string? RealName,
    string? Location,
    string? Bio,
    string TimeZoneId,
    TemperatureUnit TemperatureUnit,
    ClockFormat ClockFormat,
    DateFormat DateFormat);

public sealed class ProfileService(IServiceProvider services)
{
    public const int MaxBioLength = 500;
    public const int MaxLocationLength = 64;
    public const int MaxRealNameLength = 64;
    public const int MaxTimeZoneIdLength = 64;

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
            TimeZoneId: user.TimeZoneId,
            ChatMessageCount: chatCount,
            TopicCount: topicCount,
            PostCount: postCount);
    }

    public async Task<ProfileUpdateResult> UpdateAsync(long userId, ProfileUpdate update, CancellationToken ct)
    {
        var realName = Trim(update.RealName);
        var location = Trim(update.Location);
        var bio = Trim(update.Bio);
        var timeZoneId = update.TimeZoneId?.Trim();

        if (realName?.Length > MaxRealNameLength)
            return new(false, $"Real name must be ≤ {MaxRealNameLength} characters.");
        if (location?.Length > MaxLocationLength)
            return new(false, $"Location must be ≤ {MaxLocationLength} characters.");
        if (bio?.Length > MaxBioLength)
            return new(false, $"Bio must be ≤ {MaxBioLength} characters.");
        if (string.IsNullOrEmpty(timeZoneId))
            return new(false, "Time zone is required.");
        if (timeZoneId.Length > MaxTimeZoneIdLength)
            return new(false, $"Time zone id must be ≤ {MaxTimeZoneIdLength} characters.");
        try { _ = TimeZoneInfo.FindSystemTimeZoneById(timeZoneId); }
        catch (TimeZoneNotFoundException) { return new(false, $"Unknown time zone '{timeZoneId}'."); }
        catch (InvalidTimeZoneException) { return new(false, $"Invalid time zone '{timeZoneId}'."); }

        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId, ct);
        if (user is null)
            return new(false, "User not found.");

        user.RealName = string.IsNullOrEmpty(realName) ? null : realName;
        user.Location = string.IsNullOrEmpty(location) ? null : location;
        user.Bio = string.IsNullOrEmpty(bio) ? null : bio;
        user.TimeZoneId = timeZoneId;
        user.TemperatureUnit = update.TemperatureUnit;
        user.ClockFormat = update.ClockFormat;
        user.DateFormat = update.DateFormat;
        await db.SaveChangesAsync(ct);
        return new(true, null);
    }

    // Renders the snapshot as the multi-line text /finger prints into the chat log. The viewer
    // (the user running /finger) is what we format timestamps against — joined/last-seen render
    // in *their* preferred zone and format, not the subject's. The subject's time zone, if set,
    // is surfaced as a "local time" line so the viewer can see what time it is for them.
    public static string FormatFinger(ProfileSnapshot p, User? viewer)
    {
        var sb = new System.Text.StringBuilder();
        sb.Append("── finger ").Append(p.Handle).Append(p.IsSysop ? " (sysop) ──\n" : " ──\n");
        sb.Append("   joined     ").AppendLine(viewer.FormatDate(p.CreatedAt));
        sb.Append("   last seen  ").AppendLine(p.LastSeenAt is { } ls
            ? viewer.FormatDateTime(ls)
            : "<never>");
        if (!string.IsNullOrEmpty(p.RealName))
            sb.Append("   real name  ").AppendLine(p.RealName);
        if (!string.IsNullOrEmpty(p.Location))
            sb.Append("   location   ").AppendLine(p.Location);
        if (!string.IsNullOrEmpty(p.Bio))
            sb.Append("   bio        ").AppendLine(p.Bio);
        if (TryRenderLocalTime(p.TimeZoneId, viewer?.ClockFormat ?? ClockFormat.Hours24, out var local))
            sb.Append("   local time ").AppendLine(local);
        sb.Append("   stats      ")
          .Append(p.ChatMessageCount).Append(" chat / ")
          .Append(p.TopicCount).Append(" topics / ")
          .Append(p.PostCount).AppendLine(" posts");
        return sb.ToString();
    }

    private static bool TryRenderLocalTime(string timeZoneId, ClockFormat clockFormat, out string rendered)
    {
        rendered = string.Empty;
        if (string.IsNullOrEmpty(timeZoneId)) return false;
        try
        {
            var tz = TimeZoneInfo.FindSystemTimeZoneById(timeZoneId);
            var local = TimeZoneInfo.ConvertTime(DateTimeOffset.UtcNow, tz);
            var clock = clockFormat == ClockFormat.Hours12 ? "h:mm tt" : "HH:mm";
            rendered = $"{local.ToString(clock, System.Globalization.CultureInfo.InvariantCulture)} ({timeZoneId})";
            return true;
        }
        catch (TimeZoneNotFoundException) { return false; }
        catch (InvalidTimeZoneException) { return false; }
    }

    private static string? Trim(string? s) => s?.Trim();
}
