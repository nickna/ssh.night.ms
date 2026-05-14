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
    public const string HttpClientName = "open-meteo";

    private readonly TtlAsyncCache<(double lat, double lon), WeatherSnapshot> _cache = new(CacheTtl);

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

    private WeatherSnapshot? CachedFallback((double lat, double lon) key, string label)
    {
        if (!_cache.TryGetAny(key, out var cached)) return null;
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

    private sealed record OpenMeteoResponse([property: JsonPropertyName("current")] OpenMeteoCurrent? Current);
    private sealed record OpenMeteoCurrent(
        [property: JsonPropertyName("temperature_2m")] double Temperature2m,
        [property: JsonPropertyName("weather_code")] int WeatherCode);
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
