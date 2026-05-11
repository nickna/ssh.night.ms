using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Night.Ms.SshServer.Providers;

// Free, key-less weather provider backed by https://api.open-meteo.com. Configure via env
// vars:
//   NIGHTMS_WEATHER_LAT     (decimal degrees, default 40.7128 = NYC)
//   NIGHTMS_WEATHER_LON     (decimal degrees, default -74.0060)
//   NIGHTMS_WEATHER_LABEL   (display name, default "New York")
// Results are cached for 10 minutes — open-meteo's terms allow generous traffic but a TUI
// session opening the news screen shouldn't trigger a fresh call every time.
public sealed class OpenMeteoWeatherProvider(IHttpClientFactory httpClientFactory, IConfiguration configuration, ILogger<OpenMeteoWeatherProvider> logger)
    : IWeatherProvider
{
    public static readonly TimeSpan CacheTtl = TimeSpan.FromMinutes(10);
    public const string HttpClientName = "open-meteo";

    private readonly object _gate = new();
    private WeatherSnapshot? _cached;

    public string LocationLabel => configuration["NIGHTMS_WEATHER_LABEL"] ?? "New York";
    public double Latitude => TryParse(configuration["NIGHTMS_WEATHER_LAT"], fallback: 40.7128);
    public double Longitude => TryParse(configuration["NIGHTMS_WEATHER_LON"], fallback: -74.0060);

    public async Task<WeatherSnapshot?> GetCurrentAsync(CancellationToken cancellationToken = default)
    {
        lock (_gate)
        {
            if (_cached is { } cached && DateTimeOffset.UtcNow - cached.FetchedAt < CacheTtl)
            {
                return cached;
            }
        }

        try
        {
            var fresh = await FetchAsync(cancellationToken).ConfigureAwait(false);
            if (fresh is not null)
            {
                lock (_gate) { _cached = fresh; }
            }
            return fresh ?? CachedFallback();
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Open-Meteo fetch failed; falling back to last cached snapshot if any.");
            return CachedFallback();
        }
    }

    private WeatherSnapshot? CachedFallback()
    {
        lock (_gate) { return _cached; }
    }

    private async Task<WeatherSnapshot?> FetchAsync(CancellationToken ct)
    {
        var lat = Latitude.ToString(CultureInfo.InvariantCulture);
        var lon = Longitude.ToString(CultureInfo.InvariantCulture);
        var uri = $"v1/forecast?latitude={lat}&longitude={lon}&current=temperature_2m,weather_code&temperature_unit=celsius";

        var http = httpClientFactory.CreateClient(HttpClientName);
        if (http.BaseAddress is null)
        {
            http.BaseAddress = new Uri("https://api.open-meteo.com/");
        }

        using var response = await http.GetAsync(uri, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        var payload = await JsonSerializer.DeserializeAsync<OpenMeteoResponse>(stream, JsonOpts, ct).ConfigureAwait(false);
        if (payload?.Current is null) return null;

        var celsius = payload.Current.Temperature2m;
        return new WeatherSnapshot(
            LocationLabel: LocationLabel,
            LatitudeDegrees: Latitude,
            LongitudeDegrees: Longitude,
            TemperatureCelsius: celsius,
            TemperatureFahrenheit: celsius * 9 / 5 + 32,
            Conditions: WmoCode(payload.Current.WeatherCode),
            FetchedAt: DateTimeOffset.UtcNow);
    }

    private static readonly JsonSerializerOptions JsonOpts = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    };

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

    private static double TryParse(string? s, double fallback) =>
        double.TryParse(s, NumberStyles.Float, CultureInfo.InvariantCulture, out var v) ? v : fallback;

    private sealed record OpenMeteoResponse([property: JsonPropertyName("current")] OpenMeteoCurrent? Current);
    private sealed record OpenMeteoCurrent(
        [property: JsonPropertyName("temperature_2m")] double Temperature2m,
        [property: JsonPropertyName("weather_code")] int WeatherCode);
}
