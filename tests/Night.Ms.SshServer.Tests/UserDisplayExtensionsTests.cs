using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;

namespace Night.Ms.SshServer.Tests;

public class UserDisplayExtensionsTests
{
    // The instant we pin every time-rendering test to: 14:30 UTC on a winter date so it's
    // unambiguous about the offset for the zones we test against.
    private static readonly DateTimeOffset Winter1430Utc =
        new(2026, 1, 15, 14, 30, 0, TimeSpan.Zero);

    [Fact]
    public void ResolveTimeZone_null_user_returns_utc()
    {
        Assert.Equal(TimeZoneInfo.Utc, ((User?)null).ResolveTimeZone());
    }

    [Fact]
    public void ResolveTimeZone_unknown_id_falls_back_to_utc()
    {
        var user = new User { Handle = "x", TimeZoneId = "Mars/Olympus_Mons" };
        Assert.Equal(TimeZoneInfo.Utc, user.ResolveTimeZone());
    }

    [Fact]
    public void ResolveTimeZone_known_iana_id_resolves()
    {
        var user = new User { Handle = "x", TimeZoneId = "America/New_York" };
        var tz = user.ResolveTimeZone();
        Assert.NotEqual(TimeZoneInfo.Utc, tz);
    }

    [Fact]
    public void FormatClock_null_user_is_utc_24h()
    {
        Assert.Equal("14:30", ((User?)null).FormatClock(Winter1430Utc));
    }

    [Fact]
    public void FormatClock_applies_timezone_and_24h_format()
    {
        var user = new User { Handle = "x", TimeZoneId = "America/New_York" };
        // NYC is UTC-5 in January.
        Assert.Equal("09:30", user.FormatClock(Winter1430Utc));
    }

    [Fact]
    public void FormatClock_12h_format()
    {
        var user = new User
        {
            Handle = "x",
            TimeZoneId = "America/New_York",
            ClockFormat = ClockFormat.Hours12,
        };
        Assert.Equal("9:30 AM", user.FormatClock(Winter1430Utc));
    }

    [Fact]
    public void FormatClockWithSeconds_includes_seconds()
    {
        var user = new User { Handle = "x", TimeZoneId = "UTC" };
        var t = new DateTimeOffset(2026, 1, 15, 14, 30, 45, TimeSpan.Zero);
        Assert.Equal("14:30:45", user.FormatClockWithSeconds(t));
    }

    [Fact]
    public void FormatDate_iso_is_default()
    {
        var user = new User { Handle = "x", TimeZoneId = "UTC" };
        Assert.Equal("2026-01-15", user.FormatDate(Winter1430Utc));
    }

    [Fact]
    public void FormatDate_us_slash()
    {
        var user = new User { Handle = "x", TimeZoneId = "UTC", DateFormat = DateFormat.UsSlash };
        Assert.Equal("1/15/2026", user.FormatDate(Winter1430Utc));
    }

    [Fact]
    public void FormatDate_eu_slash()
    {
        var user = new User { Handle = "x", TimeZoneId = "UTC", DateFormat = DateFormat.EuSlash };
        Assert.Equal("15/1/2026", user.FormatDate(Winter1430Utc));
    }

    [Fact]
    public void FormatDate_applies_timezone_before_format()
    {
        // 02:30 UTC on Jan 16 → still Jan 15 in NYC (UTC-5).
        var user = new User { Handle = "x", TimeZoneId = "America/New_York" };
        var t = new DateTimeOffset(2026, 1, 16, 2, 30, 0, TimeSpan.Zero);
        Assert.Equal("2026-01-15", user.FormatDate(t));
    }

    [Fact]
    public void FormatDateTime_combines_date_and_clock()
    {
        var user = new User
        {
            Handle = "x",
            TimeZoneId = "America/New_York",
            DateFormat = DateFormat.UsSlash,
            ClockFormat = ClockFormat.Hours12,
        };
        Assert.Equal("1/15/2026 9:30 AM", user.FormatDateTime(Winter1430Utc));
    }

    [Fact]
    public void FormatDateTime_null_user_iso_24h_utc()
    {
        Assert.Equal("2026-01-15 14:30", ((User?)null).FormatDateTime(Winter1430Utc));
    }

    [Fact]
    public void FormatTemperature_celsius_default()
    {
        var snap = SampleWeather();
        var user = new User { Handle = "x" };
        Assert.Equal("22°C", user.FormatTemperature(snap));
    }

    [Fact]
    public void FormatTemperature_fahrenheit()
    {
        var snap = SampleWeather();
        var user = new User { Handle = "x", TemperatureUnit = TemperatureUnit.Fahrenheit };
        Assert.Equal("72°F", user.FormatTemperature(snap));
    }

    [Fact]
    public void FormatTemperature_both()
    {
        var snap = SampleWeather();
        var user = new User { Handle = "x", TemperatureUnit = TemperatureUnit.Both };
        Assert.Equal("22°C/72°F", user.FormatTemperature(snap));
    }

    [Fact]
    public void FormatTemperature_null_user_defaults_to_celsius()
    {
        var snap = SampleWeather();
        Assert.Equal("22°C", ((User?)null).FormatTemperature(snap));
    }

    [Fact]
    public void FormatClock_dst_spring_forward_renders_in_local_zone()
    {
        // 2026-03-08 02:30 EST → DST jumps clocks from 02:00 to 03:00. 07:30 UTC on that date
        // is 03:30 EDT (post-jump). The formatter should reflect that.
        var user = new User { Handle = "x", TimeZoneId = "America/New_York" };
        var t = new DateTimeOffset(2026, 3, 8, 7, 30, 0, TimeSpan.Zero);
        Assert.Equal("03:30", user.FormatClock(t));
    }

    private static WeatherSnapshot SampleWeather() => new(
        LocationLabel: "Test City",
        LatitudeDegrees: 0,
        LongitudeDegrees: 0,
        TemperatureCelsius: 22.0,
        TemperatureFahrenheit: 71.6,
        Conditions: "Clear",
        FetchedAt: DateTimeOffset.UtcNow);
}
