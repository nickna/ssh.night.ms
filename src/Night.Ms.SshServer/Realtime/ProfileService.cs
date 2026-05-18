using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;

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
    int PostCount,
    // ProfilePictureUpdatedAt doubles as a "has one" flag and an ETag basis for the avatar
    // endpoint. Null = no upload; the rendering layer falls back to a procedural identicon.
    DateTimeOffset? ProfilePictureUpdatedAt);

// Returned alongside an UpdateAsync failure when the typed Location couldn't be geocoded.
// The screen uses this to decide whether to offer the "use IP-detected location" prompt.
public enum ProfileUpdateFailure
{
    None = 0,
    LocationNotFound = 1,
    LocationServiceUnavailable = 2,
}

public sealed record ProfileUpdateResult(
    bool Ok,
    string? Error,
    ProfileUpdateFailure Failure = ProfileUpdateFailure.None,
    // Populated on success when the user has a geocoded location, so the screen can refresh
    // the in-memory User without re-querying the database.
    double? LocationLatitude = null,
    double? LocationLongitude = null,
    string? LocationCanonical = null,
    LocationSource LocationSource = LocationSource.None);

// A coords-already-resolved location to apply without re-geocoding. Used by the
// "accept IP suggestion" flow on profile-save failure so the screen can submit a typed
// label plus its known-good coords in one round-trip.
public sealed record PreResolvedLocation(double Latitude, double Longitude, string Canonical, LocationSource Source);

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
    DateFormat DateFormat,
    PreResolvedLocation? PreResolvedLocation = null);

public sealed class ProfileService(IDbContextFactory<AppDbContext> dbFactory, IGeocodingProvider geocoder)
{
    public const int MaxBioLength = 500;
    public const int MaxLocationLength = 64;
    public const int MaxRealNameLength = 64;
    public const int MaxTimeZoneIdLength = 64;

    // Bulk "does this handle have a profile picture?" lookup. One round-trip vs. one query
    // per handle (and one Task.Run per fresh handle) — used by ChatScreen to prime its pfp
    // cache when a channel's history loads, so the on-screen "●" marker is correct on
    // first paint instead of trickling in over the next few hundred milliseconds.
    public async Task<IReadOnlyDictionary<string, bool>> BatchHasPfpAsync(
        IReadOnlyCollection<string> handles, CancellationToken ct)
    {
        if (handles.Count == 0) return new Dictionary<string, bool>();
        await using var db = await dbFactory.CreateDbContextAsync(ct);
        // citext column makes the comparison case-insensitive; we still return the row's
        // canonical Handle so the caller's dict keys match what they sent (the caller uses
        // OrdinalIgnoreCase, so case differences fold there too).
        var rows = await db.Users.AsNoTracking()
            .Where(u => handles.Contains(u.Handle))
            .Select(u => new { u.Handle, HasPfp = u.ProfilePictureUpdatedAt != null })
            .ToListAsync(ct);
        var result = new Dictionary<string, bool>(rows.Count, StringComparer.OrdinalIgnoreCase);
        foreach (var r in rows) result[r.Handle] = r.HasPfp;
        return result;
    }

    // Looks up a profile by handle, including derived counts. Returns null when the user
    // doesn't exist (the caller decides how to render that — /finger prints "no such user").
    public async Task<ProfileSnapshot?> GetByHandleAsync(string handle, CancellationToken ct)
    {
        await using var db = await dbFactory.CreateDbContextAsync(ct);

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
            PostCount: postCount,
            ProfilePictureUpdatedAt: user.ProfilePictureUpdatedAt);
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

        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId, ct);
        if (user is null)
            return new(false, "User not found.");

        // Resolve the new geocoded fields. Only re-geocode when the typed Location actually
        // changed; this keeps an existing IP-guess source intact across unrelated profile
        // saves (e.g. user edits their bio but didn't touch location).
        var locationChanged = !string.Equals(user.Location, location, StringComparison.Ordinal);
        if (locationChanged)
        {
            if (string.IsNullOrEmpty(location))
            {
                user.LocationLatitude = null;
                user.LocationLongitude = null;
                user.LocationCanonical = null;
                user.LocationSource = LocationSource.None;
            }
            else if (update.PreResolvedLocation is { } pre)
            {
                user.LocationLatitude = pre.Latitude;
                user.LocationLongitude = pre.Longitude;
                user.LocationCanonical = pre.Canonical;
                user.LocationSource = pre.Source;
            }
            else
            {
                var matches = await geocoder.SearchAsync(location, ct);
                if (matches is null)
                    return new(false, "Couldn't reach the geocoding service — try again in a moment.", ProfileUpdateFailure.LocationServiceUnavailable);
                if (matches.Count == 0)
                    return new(false, $"Couldn't find '{location}'. Try 'City, Country'.", ProfileUpdateFailure.LocationNotFound);

                var top = matches[0];
                user.LocationLatitude = top.Latitude;
                user.LocationLongitude = top.Longitude;
                user.LocationCanonical = top.CanonicalName;
                user.LocationSource = LocationSource.Manual;
            }
        }

        user.RealName = string.IsNullOrEmpty(realName) ? null : realName;
        user.Location = string.IsNullOrEmpty(location) ? null : location;
        user.Bio = string.IsNullOrEmpty(bio) ? null : bio;
        user.TimeZoneId = timeZoneId;
        user.TemperatureUnit = update.TemperatureUnit;
        user.ClockFormat = update.ClockFormat;
        user.DateFormat = update.DateFormat;
        await db.SaveChangesAsync(ct);
        return new ProfileUpdateResult(
            Ok: true,
            Error: null,
            Failure: ProfileUpdateFailure.None,
            LocationLatitude: user.LocationLatitude,
            LocationLongitude: user.LocationLongitude,
            LocationCanonical: user.LocationCanonical,
            LocationSource: user.LocationSource);
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
