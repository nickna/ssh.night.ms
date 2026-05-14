namespace Night.Ms.SshServer.Providers;

// Folds Open-Meteo's WMO weather codes into the smaller WeatherCondition set that the
// animated banner cares about. WMO defines distinct codes for "rain showers (slight)",
// "rain showers (violent)", etc.; the renderer doesn't have separate art for every shade,
// so we collapse them into a coarser bucket.
//
// Day/night split exists only for sky-driven conditions (clear, partly cloudy) where the
// scene looks visibly different at night. Heavily-occluded conditions (rain / snow / fog /
// thunderstorm) reuse a single scene across day and night — sky color isn't the salient
// feature there, the precipitation is.
public static class WeatherConditionMapper
{
    public static WeatherCondition Map(int wmoCode, bool isDay) => wmoCode switch
    {
        // 0 = clear sky, 1 = mainly clear. Both collapse to the same animation.
        0 or 1 => isDay ? WeatherCondition.ClearDay : WeatherCondition.ClearNight,
        2 => isDay ? WeatherCondition.PartlyCloudyDay : WeatherCondition.PartlyCloudyNight,
        3 => WeatherCondition.Cloudy,
        45 or 48 => WeatherCondition.Fog,
        51 or 53 or 55 or 56 or 57 => WeatherCondition.Drizzle,
        61 or 63 or 65 or 66 or 67 or 80 or 81 or 82 => WeatherCondition.Rain,
        71 or 73 or 75 or 77 or 85 or 86 => WeatherCondition.Snow,
        95 or 96 or 99 => WeatherCondition.Thunderstorm,
        _ => WeatherCondition.Unknown,
    };

    // Filesystem-safe slug used to find the matching `art/weather/{slug}/frame-*.ans`
    // directory. The slug is kept hyphen-cased rather than the enum's PascalCase so a
    // sysop browsing the asset directory sees names that match how condition labels are
    // usually written.
    public static string ToSlug(this WeatherCondition condition) => condition switch
    {
        WeatherCondition.ClearDay => "clear-day",
        WeatherCondition.ClearNight => "clear-night",
        WeatherCondition.PartlyCloudyDay => "partly-cloudy-day",
        WeatherCondition.PartlyCloudyNight => "partly-cloudy-night",
        WeatherCondition.Cloudy => "cloudy",
        WeatherCondition.Fog => "fog",
        WeatherCondition.Drizzle => "drizzle",
        WeatherCondition.Rain => "rain",
        WeatherCondition.Snow => "snow",
        WeatherCondition.Thunderstorm => "thunderstorm",
        _ => "default",
    };
}
