using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;
using Night.Ms.SshServer.Caching;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Providers;

// Free, key-less weather provider backed by https://api.open-meteo.com. Per-user coords
// flow in from the caller (User.LocationLatitude/Longitude); when not supplied the
// configured fallback (env vars below, default NYC) is used. Results are cached per
// rounded coordinate for 10 minutes so multiple sessions sharing a location share a
// single upstream call inside that window.
//   NIGHTMS_WEATHER_LAT     (decimal degrees, default 40.7128 = NYC)
//   NIGHTMS_WEATHER_LON     (decimal degrees, default -74.0060)
//   NIGHTMS_WEATHER_LABEL   (display name, default "New York")
public sealed class OpenMeteoWeatherProvider(IHttpClientFactory httpClientFactory, NightMsOptions options, ILogger<OpenMeteoWeatherProvider> logger)
    : IWeatherProvider
{
    public static readonly TimeSpan CacheTtl = TimeSpan.FromMinutes(10);
    // Forecast windows don't shift inside half-hour granularity at the source. Hold longer
    // than current conditions so multiple sessions opening the screen back-to-back share
    // one upstream call.
    public static readonly TimeSpan ForecastCacheTtl = TimeSpan.FromMinutes(30);
    public const string HttpClientName = "open-meteo";

    private readonly TtlAsyncCache<(double lat, double lon), WeatherSnapshot> _cache = new(CacheTtl);
    // Coarser cache key (2 decimals, ~1.1 km) so sessions in the same neighborhood share
    // the cached forecast even though their stored coords differ slightly.
    private readonly TtlAsyncCache<(double lat, double lon), WeatherForecast> _forecastCache = new(ForecastCacheTtl);

    public string FallbackLabel => options.WeatherLabel ?? "New York";
    public double FallbackLatitude => options.WeatherLatitude ?? 40.7128;
    public double FallbackLongitude => options.WeatherLongitude ?? -74.0060;

    public async Task<WeatherSnapshot?> GetCurrentAsync(
        double? latitude = null,
        double? longitude = null,
        string? label = null,
        CancellationToken cancellationToken = default)
    {
        var lat = latitude ?? FallbackLatitude;
        var lon = longitude ?? FallbackLongitude;
        var displayLabel = !string.IsNullOrEmpty(label) ? label : FallbackLabel;
        var cacheKey = (Math.Round(lat, 3), Math.Round(lon, 3));

        if (_cache.TryGetFresh(cacheKey, out var cached))
        {
            // Refresh the label so a user editing their location sees the new name even when
            // we serve a cached temperature; coords haven't changed so the reading is valid.
            return cached.LocationLabel == displayLabel ? cached : cached with { LocationLabel = displayLabel };
        }

        try
        {
            var fresh = await FetchAsync(lat, lon, displayLabel, cancellationToken).ConfigureAwait(false);
            if (fresh is not null)
            {
                _cache.Set(cacheKey, fresh);
            }
            return fresh ?? CachedFallback(cacheKey, displayLabel);
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Open-Meteo fetch failed for {Lat},{Lon}; falling back to last cached snapshot if any.", lat, lon);
            return CachedFallback(cacheKey, displayLabel);
        }
    }

    public async Task<WeatherForecast?> GetForecastAsync(
        double? latitude = null,
        double? longitude = null,
        string? label = null,
        CancellationToken cancellationToken = default)
    {
        var lat = latitude ?? FallbackLatitude;
        var lon = longitude ?? FallbackLongitude;
        var displayLabel = !string.IsNullOrEmpty(label) ? label : FallbackLabel;
        var cacheKey = (Math.Round(lat, 2), Math.Round(lon, 2));

        if (_forecastCache.TryGetFresh(cacheKey, out var cached))
        {
            return cached.LocationLabel == displayLabel ? cached : cached with { LocationLabel = displayLabel };
        }

        try
        {
            var fresh = await FetchForecastAsync(lat, lon, displayLabel, cancellationToken).ConfigureAwait(false);
            if (fresh is not null)
            {
                _forecastCache.Set(cacheKey, fresh);
            }
            return fresh ?? CachedForecastFallback(cacheKey, displayLabel);
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Open-Meteo forecast fetch failed for {Lat},{Lon}; falling back to last cached forecast if any.", lat, lon);
            return CachedForecastFallback(cacheKey, displayLabel);
        }
    }

    private WeatherSnapshot? CachedFallback((double lat, double lon) key, string label)
    {
        if (!_cache.TryGetAny(key, out var cached)) return null;
        return cached.LocationLabel == label ? cached : cached with { LocationLabel = label };
    }

    private WeatherForecast? CachedForecastFallback((double lat, double lon) key, string label)
    {
        if (!_forecastCache.TryGetAny(key, out var cached)) return null;
        return cached.LocationLabel == label ? cached : cached with { LocationLabel = label };
    }

    private async Task<WeatherSnapshot?> FetchAsync(double latitude, double longitude, string label, CancellationToken ct)
    {
        var lat = latitude.ToString(CultureInfo.InvariantCulture);
        var lon = longitude.ToString(CultureInfo.InvariantCulture);
        var uri = $"v1/forecast?latitude={lat}&longitude={lon}&current=temperature_2m,weather_code&temperature_unit=celsius";

        var http = httpClientFactory.CreateClient(HttpClientName);
        using var response = await http.GetAsync(uri, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        var payload = await JsonSerializer.DeserializeAsync<OpenMeteoResponse>(stream, SnakeCaseJson.Options, ct).ConfigureAwait(false);
        if (payload?.Current is null) return null;

        var celsius = payload.Current.Temperature2m;
        return new WeatherSnapshot(
            LocationLabel: label,
            LatitudeDegrees: latitude,
            LongitudeDegrees: longitude,
            TemperatureCelsius: celsius,
            TemperatureFahrenheit: celsius * 9 / 5 + 32,
            Conditions: WmoCode(payload.Current.WeatherCode),
            FetchedAt: DateTimeOffset.UtcNow);
    }

    // WMO 4677 weather codes — see https://open-meteo.com/en/docs (look for "Weather variable documentation").
    internal static string WmoCode(int code) => code switch
    {
        0 => "Clear sky",
        1 => "Mainly clear",
        2 => "Partly cloudy",
        3 => "Overcast",
        45 or 48 => "Fog",
        51 or 53 or 55 => "Drizzle",
        56 or 57 => "Freezing drizzle",
        61 or 63 or 65 => "Rain",
        66 or 67 => "Freezing rain",
        71 or 73 or 75 => "Snow",
        77 => "Snow grains",
        80 or 81 or 82 => "Rain showers",
        85 or 86 => "Snow showers",
        95 => "Thunderstorm",
        96 or 99 => "Thunderstorm with hail",
        _ => $"Code {code}",
    };

    private async Task<WeatherForecast?> FetchForecastAsync(double latitude, double longitude, string label, CancellationToken ct)
    {
        var lat = latitude.ToString(CultureInfo.InvariantCulture);
        var lon = longitude.ToString(CultureInfo.InvariantCulture);
        // forecast_hours=24 limits hourly to the next 24 hours (default is 168). forecast_days=7
        // gives the standard week of daily summaries. timezone=auto returns timestamps in the
        // location's local time, which is what the screen displays.
        var uri =
            $"v1/forecast?latitude={lat}&longitude={lon}" +
            "&current=temperature_2m,relative_humidity_2m,apparent_temperature,is_day,precipitation,weather_code,wind_speed_10m,wind_direction_10m" +
            "&hourly=temperature_2m,precipitation_probability,precipitation,weather_code,is_day" +
            "&daily=weather_code,temperature_2m_max,temperature_2m_min,sunrise,sunset,uv_index_max,precipitation_sum,precipitation_probability_max" +
            "&temperature_unit=celsius&wind_speed_unit=kmh&precipitation_unit=mm" +
            "&forecast_hours=24&forecast_days=7&timezone=auto";

        var http = httpClientFactory.CreateClient(HttpClientName);
        using var response = await http.GetAsync(uri, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        var payload = await JsonSerializer.DeserializeAsync<OpenMeteoForecastResponse>(stream, SnakeCaseJson.Options, ct).ConfigureAwait(false);
        if (payload is null || payload.Current is null || payload.Daily is null) return null;

        var current = BuildCurrent(payload.Current);
        var hourly = BuildHourly(payload.Hourly, payload.UtcOffsetSeconds);
        var daily = BuildDaily(payload.Daily, payload.UtcOffsetSeconds);

        return new WeatherForecast(
            LocationLabel: label,
            LatitudeDegrees: latitude,
            LongitudeDegrees: longitude,
            TimeZone: payload.Timezone,
            Current: current,
            Hourly: hourly,
            Daily: daily,
            FetchedAt: DateTimeOffset.UtcNow);
    }

    private static CurrentConditions BuildCurrent(OpenMeteoForecastCurrent c)
    {
        var apparent = c.ApparentTemperature ?? c.Temperature2m;
        var humidity = c.RelativeHumidity2m ?? 0;
        var wind = c.WindSpeed10m ?? 0;
        var windDir = c.WindDirection10m ?? 0;
        var precip = c.Precipitation ?? 0;
        return new CurrentConditions(
            TemperatureCelsius: c.Temperature2m,
            TemperatureFahrenheit: CtoF(c.Temperature2m),
            ApparentTemperatureCelsius: apparent,
            ApparentTemperatureFahrenheit: CtoF(apparent),
            RelativeHumidityPercent: humidity,
            WindSpeedKph: wind,
            WindSpeedMph: KphToMph(wind),
            WindDirectionDegrees: windDir,
            PrecipitationMm: precip,
            PrecipitationInches: MmToInches(precip),
            WeatherCode: c.WeatherCode,
            IsDay: c.IsDay == 1,
            Conditions: WmoCode(c.WeatherCode));
    }

    private static IReadOnlyList<HourlyForecast> BuildHourly(OpenMeteoForecastHourly? h, int utcOffsetSeconds)
    {
        if (h?.Time is null || h.Time.Length == 0) return Array.Empty<HourlyForecast>();
        var n = h.Time.Length;
        var offset = TimeSpan.FromSeconds(utcOffsetSeconds);
        var hourly = new List<HourlyForecast>(n);
        for (var i = 0; i < n; i++)
        {
            var temp = ValueAt(h.Temperature2m, i);
            var pp = (int)Math.Round(ValueAt(h.PrecipitationProbability, i));
            var precip = ValueAt(h.Precipitation, i);
            var code = (int)ValueAt(h.WeatherCode, i);
            var isDay = (int)ValueAt(h.IsDay, i) == 1;
            hourly.Add(new HourlyForecast(
                Time: ParseLocal(h.Time[i], offset),
                TemperatureCelsius: temp,
                TemperatureFahrenheit: CtoF(temp),
                PrecipitationProbabilityPercent: pp,
                PrecipitationMm: precip,
                PrecipitationInches: MmToInches(precip),
                WeatherCode: code,
                IsDay: isDay,
                Conditions: WmoCode(code)));
        }
        return hourly;
    }

    private static IReadOnlyList<DailyForecast> BuildDaily(OpenMeteoForecastDaily d, int utcOffsetSeconds)
    {
        if (d.Time is null || d.Time.Length == 0) return Array.Empty<DailyForecast>();
        var n = d.Time.Length;
        var offset = TimeSpan.FromSeconds(utcOffsetSeconds);
        var daily = new List<DailyForecast>(n);
        for (var i = 0; i < n; i++)
        {
            var max = ValueAt(d.Temperature2mMax, i);
            var min = ValueAt(d.Temperature2mMin, i);
            var sum = ValueAt(d.PrecipitationSum, i);
            var ppMax = (int)Math.Round(ValueAt(d.PrecipitationProbabilityMax, i));
            var uv = ValueAt(d.UvIndexMax, i);
            var code = (int)ValueAt(d.WeatherCode, i);
            daily.Add(new DailyForecast(
                Date: DateOnly.Parse(d.Time[i], CultureInfo.InvariantCulture),
                Sunrise: ParseLocal(StringAt(d.Sunrise, i), offset),
                Sunset: ParseLocal(StringAt(d.Sunset, i), offset),
                TemperatureMaxCelsius: max,
                TemperatureMaxFahrenheit: CtoF(max),
                TemperatureMinCelsius: min,
                TemperatureMinFahrenheit: CtoF(min),
                PrecipitationProbabilityMaxPercent: ppMax,
                PrecipitationSumMm: sum,
                PrecipitationSumInches: MmToInches(sum),
                UvIndexMax: uv,
                WeatherCode: code,
                Conditions: WmoCode(code)));
        }
        return daily;
    }

    private static double ValueAt(double[]? arr, int i) => arr is { Length: > 0 } && i < arr.Length ? arr[i] : 0d;
    private static string StringAt(string[]? arr, int i) => arr is { Length: > 0 } && i < arr.Length ? arr[i] : string.Empty;

    private static double CtoF(double c) => c * 9 / 5 + 32;
    private static double KphToMph(double kph) => kph / 1.609344;
    private static double MmToInches(double mm) => mm / 25.4;

    // Open-Meteo emits times in local wall-clock when timezone=auto (e.g. "2026-05-14T15:00"
    // with no offset suffix). Pair them with utc_offset_seconds to produce a fully-qualified
    // DateTimeOffset that renders correctly without a tz lookup on every cell.
    private static DateTimeOffset ParseLocal(string value, TimeSpan offset)
    {
        if (string.IsNullOrEmpty(value)) return DateTimeOffset.MinValue;
        var dt = DateTime.Parse(value, CultureInfo.InvariantCulture, DateTimeStyles.AssumeLocal);
        return new DateTimeOffset(DateTime.SpecifyKind(dt, DateTimeKind.Unspecified), offset);
    }

    private sealed record OpenMeteoResponse([property: JsonPropertyName("current")] OpenMeteoCurrent? Current);
    private sealed record OpenMeteoCurrent(
        [property: JsonPropertyName("temperature_2m")] double Temperature2m,
        [property: JsonPropertyName("weather_code")] int WeatherCode);

    private sealed record OpenMeteoForecastResponse(
        [property: JsonPropertyName("timezone")] string? Timezone,
        [property: JsonPropertyName("utc_offset_seconds")] int UtcOffsetSeconds,
        [property: JsonPropertyName("current")] OpenMeteoForecastCurrent? Current,
        [property: JsonPropertyName("hourly")] OpenMeteoForecastHourly? Hourly,
        [property: JsonPropertyName("daily")] OpenMeteoForecastDaily? Daily);

    private sealed record OpenMeteoForecastCurrent(
        [property: JsonPropertyName("temperature_2m")] double Temperature2m,
        [property: JsonPropertyName("relative_humidity_2m")] int? RelativeHumidity2m,
        [property: JsonPropertyName("apparent_temperature")] double? ApparentTemperature,
        [property: JsonPropertyName("is_day")] int? IsDay,
        [property: JsonPropertyName("precipitation")] double? Precipitation,
        [property: JsonPropertyName("weather_code")] int WeatherCode,
        [property: JsonPropertyName("wind_speed_10m")] double? WindSpeed10m,
        [property: JsonPropertyName("wind_direction_10m")] int? WindDirection10m);

    private sealed record OpenMeteoForecastHourly(
        [property: JsonPropertyName("time")] string[]? Time,
        [property: JsonPropertyName("temperature_2m")] double[]? Temperature2m,
        [property: JsonPropertyName("precipitation_probability")] double[]? PrecipitationProbability,
        [property: JsonPropertyName("precipitation")] double[]? Precipitation,
        [property: JsonPropertyName("weather_code")] double[]? WeatherCode,
        [property: JsonPropertyName("is_day")] double[]? IsDay);

    private sealed record OpenMeteoForecastDaily(
        [property: JsonPropertyName("time")] string[]? Time,
        [property: JsonPropertyName("weather_code")] double[]? WeatherCode,
        [property: JsonPropertyName("temperature_2m_max")] double[]? Temperature2mMax,
        [property: JsonPropertyName("temperature_2m_min")] double[]? Temperature2mMin,
        [property: JsonPropertyName("sunrise")] string[]? Sunrise,
        [property: JsonPropertyName("sunset")] string[]? Sunset,
        [property: JsonPropertyName("uv_index_max")] double[]? UvIndexMax,
        [property: JsonPropertyName("precipitation_sum")] double[]? PrecipitationSum,
        [property: JsonPropertyName("precipitation_probability_max")] double[]? PrecipitationProbabilityMax);
}

public static class OpenMeteoWeatherProviderRegistration
{
    // Wires the named HttpClient + IWeatherProvider→OpenMeteoWeatherProvider binding so
    // Program.cs doesn't need to know the API base URL or HTTP-client name. Re-bind
    // IWeatherProvider after this call to swap implementations.
    public static IServiceCollection AddOpenMeteoWeather(this IServiceCollection services)
    {
        services.AddHttpClient(OpenMeteoWeatherProvider.HttpClientName, c =>
            c.BaseAddress = new Uri("https://api.open-meteo.com/"));
        services.AddSingleton<IWeatherProvider, OpenMeteoWeatherProvider>();
        return services;
    }
}
