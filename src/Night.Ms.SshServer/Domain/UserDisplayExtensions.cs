using System.Globalization;
using Night.Ms.SshServer.Providers;

namespace Night.Ms.SshServer.Domain;

// Centralizes all per-user rendering of times, dates, and temperatures. Every screen that
// surfaces a timestamp or weather reading routes through here so the user's preferences
// (time zone, 12/24-hour clock, date format, temperature unit) are honored consistently.
//
// Accepting `User?` lets pre-login screens (RegisterScreen) call these without special-casing —
// a null user falls back to the same neutral global defaults the database hands new rows.
public static class UserDisplayExtensions
{
    // Resolves the user's stored IANA id to a TimeZoneInfo. Stale/unknown ids (e.g. a zone that
    // was retired after the row was written) silently fall back to UTC rather than throwing —
    // a broken tz id should never break the screen rendering it.
    public static TimeZoneInfo ResolveTimeZone(this User? user)
    {
        var id = user?.TimeZoneId;
        if (string.IsNullOrEmpty(id)) return TimeZoneInfo.Utc;
        try { return TimeZoneInfo.FindSystemTimeZoneById(id); }
        catch (TimeZoneNotFoundException) { return TimeZoneInfo.Utc; }
        catch (InvalidTimeZoneException) { return TimeZoneInfo.Utc; }
    }

    // Wall-clock time only — "HH:mm" or "h:mm tt".
    public static string FormatClock(this User? user, DateTimeOffset value)
    {
        var local = TimeZoneInfo.ConvertTime(value, user.ResolveTimeZone());
        var format = (user?.ClockFormat ?? ClockFormat.Hours24) == ClockFormat.Hours12
            ? "h:mm tt"
            : "HH:mm";
        return local.ToString(format, CultureInfo.InvariantCulture);
    }

    // Wall-clock time with seconds — only used by the status-bar clock.
    public static string FormatClockWithSeconds(this User? user, DateTimeOffset value)
    {
        var local = TimeZoneInfo.ConvertTime(value, user.ResolveTimeZone());
        var format = (user?.ClockFormat ?? ClockFormat.Hours24) == ClockFormat.Hours12
            ? "h:mm:ss tt"
            : "HH:mm:ss";
        return local.ToString(format, CultureInfo.InvariantCulture);
    }

    public static string FormatDate(this User? user, DateTimeOffset value)
    {
        var local = TimeZoneInfo.ConvertTime(value, user.ResolveTimeZone());
        return local.ToString(DatePattern(user?.DateFormat ?? DateFormat.Iso), CultureInfo.InvariantCulture);
    }

    public static string FormatDateTime(this User? user, DateTimeOffset value)
    {
        var local = TimeZoneInfo.ConvertTime(value, user.ResolveTimeZone());
        var date = DatePattern(user?.DateFormat ?? DateFormat.Iso);
        var clock = (user?.ClockFormat ?? ClockFormat.Hours24) == ClockFormat.Hours12 ? "h:mm tt" : "HH:mm";
        return local.ToString($"{date} {clock}", CultureInfo.InvariantCulture);
    }

    // Returns just the temperature portion (no location or conditions) so the caller can
    // compose the full status string. "22°C", "72°F", or "22°C/72°F" depending on the unit pref.
    public static string FormatTemperature(this User? user, WeatherSnapshot snap) =>
        (user?.TemperatureUnit ?? TemperatureUnit.Celsius) switch
        {
            TemperatureUnit.Fahrenheit => $"{snap.TemperatureFahrenheit:F0}°F",
            TemperatureUnit.Both => $"{snap.TemperatureCelsius:F0}°C/{snap.TemperatureFahrenheit:F0}°F",
            _ => $"{snap.TemperatureCelsius:F0}°C",
        };

    private static string DatePattern(DateFormat format) => format switch
    {
        DateFormat.UsSlash => "M/d/yyyy",
        DateFormat.EuSlash => "d/M/yyyy",
        _ => "yyyy-MM-dd",
    };
}
