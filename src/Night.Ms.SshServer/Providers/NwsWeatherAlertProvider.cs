using System.Globalization;
using System.Net;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Caching;

namespace Night.Ms.SshServer.Providers;

public sealed class NwsWeatherAlertProvider(
    IHttpClientFactory httpClientFactory,
    ILogger<NwsWeatherAlertProvider> logger) : IWeatherAlertProvider
{
    internal const string HttpClientName = "nws";
    private static readonly TimeSpan CacheTtl = TimeSpan.FromMinutes(5);
    private static readonly JsonSerializerOptions JsonOptions = new() { PropertyNameCaseInsensitive = true };

    private readonly TtlAsyncCache<(double lat, double lon), IReadOnlyList<WeatherAlert>> _cache = new(CacheTtl);

    public async Task<IReadOnlyList<WeatherAlert>> GetActiveAlertsAsync(
        double latitude, double longitude, CancellationToken cancellationToken = default)
    {
        var lat = Math.Round(latitude, 2);
        var lon = Math.Round(longitude, 2);
        var key = (lat, lon);

        if (_cache.TryGetFresh(key, out var cached))
            return cached;

        try
        {
            var alerts = await FetchAsync(lat, lon, cancellationToken).ConfigureAwait(false);
            _cache.Set(key, alerts);
            return alerts;
        }
        catch (HttpRequestException ex) when (ex.StatusCode is HttpStatusCode.NotFound or HttpStatusCode.BadRequest)
        {
            var empty = Array.Empty<WeatherAlert>();
            _cache.Set(key, empty);
            return empty;
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "NWS alerts fetch failed for ({Lat}, {Lon})", lat, lon);
            return _cache.TryGetAny(key, out var stale) ? stale : [];
        }
    }

    private async Task<IReadOnlyList<WeatherAlert>> FetchAsync(double lat, double lon, CancellationToken ct)
    {
        var latStr = lat.ToString(CultureInfo.InvariantCulture);
        var lonStr = lon.ToString(CultureInfo.InvariantCulture);
        var uri = $"alerts/active?point={latStr},{lonStr}";

        var http = httpClientFactory.CreateClient(HttpClientName);
        using var response = await http.GetAsync(uri, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        var payload = await JsonSerializer.DeserializeAsync<NwsAlertResponse>(stream, JsonOptions, ct).ConfigureAwait(false);

        if (payload?.Features is null or { Count: 0 })
            return [];

        var results = new List<WeatherAlert>();
        foreach (var feature in payload.Features)
        {
            var props = feature.Properties;
            if (props is null) continue;
            if (!string.Equals(props.Status, "Actual", StringComparison.OrdinalIgnoreCase)) continue;

            results.Add(new WeatherAlert(
                Id: props.Id ?? string.Empty,
                Event: props.Event ?? "Unknown",
                Severity: ParseSeverity(props.Severity),
                Headline: props.Headline ?? props.Event ?? "Weather Alert",
                Description: props.Description ?? string.Empty,
                AreaDescription: props.AreaDesc ?? string.Empty,
                Effective: props.Effective ?? DateTimeOffset.UtcNow,
                Expires: props.Expires ?? DateTimeOffset.UtcNow.AddHours(1)));
        }

        results.Sort((a, b) => b.Severity.CompareTo(a.Severity));
        return results;
    }

    internal static AlertSeverity ParseSeverity(string? severity) => severity?.ToLowerInvariant() switch
    {
        "extreme" => AlertSeverity.Extreme,
        "severe" => AlertSeverity.Severe,
        "moderate" => AlertSeverity.Moderate,
        "minor" => AlertSeverity.Minor,
        _ => AlertSeverity.Unknown,
    };

    private sealed record NwsAlertResponse(
        [property: JsonPropertyName("features")] IReadOnlyList<NwsFeature>? Features);

    private sealed record NwsFeature(
        [property: JsonPropertyName("properties")] NwsProperties? Properties);

    private sealed record NwsProperties
    {
        [JsonPropertyName("id")] public string? Id { get; init; }
        [JsonPropertyName("event")] public string? Event { get; init; }
        [JsonPropertyName("severity")] public string? Severity { get; init; }
        [JsonPropertyName("headline")] public string? Headline { get; init; }
        [JsonPropertyName("description")] public string? Description { get; init; }
        [JsonPropertyName("areaDesc")] public string? AreaDesc { get; init; }
        [JsonPropertyName("effective")] public DateTimeOffset? Effective { get; init; }
        [JsonPropertyName("expires")] public DateTimeOffset? Expires { get; init; }
        [JsonPropertyName("status")] public string? Status { get; init; }
    }
}

public static class NwsWeatherAlertProviderRegistration
{
    public static IServiceCollection AddNwsWeatherAlerts(this IServiceCollection services)
    {
        services.AddHttpClient(NwsWeatherAlertProvider.HttpClientName, c =>
        {
            c.BaseAddress = new Uri("https://api.weather.gov/");
            c.DefaultRequestHeaders.UserAgent.ParseAdd("ssh.night.ms/1.0 (nick@night.ms)");
        });
        services.AddSingleton<IWeatherAlertProvider, NwsWeatherAlertProvider>();
        return services;
    }
}
