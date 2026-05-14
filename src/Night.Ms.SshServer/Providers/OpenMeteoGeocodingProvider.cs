using System.Net;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Night.Ms.SshServer.Providers;

// Forward + reverse geocoding via Open-Meteo's free, key-less geocoding API
// (https://open-meteo.com/en/docs/geocoding-api). Same vendor as the weather provider, so
// "what we look up" and "what we resolve" stay consistent.
public sealed class OpenMeteoGeocodingProvider(IHttpClientFactory httpClientFactory, ILogger<OpenMeteoGeocodingProvider> logger)
    : IGeocodingProvider
{
    public const string HttpClientName = "open-meteo-geocoding";

    public async Task<IReadOnlyList<GeocodingMatch>?> SearchAsync(string query, CancellationToken cancellationToken = default)
    {
        var trimmed = query?.Trim();
        if (string.IsNullOrEmpty(trimmed))
            return Array.Empty<GeocodingMatch>();

        var uri = $"v1/search?name={WebUtility.UrlEncode(trimmed)}&count=5&language=en&format=json";
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            using var response = await http.GetAsync(uri, cancellationToken).ConfigureAwait(false);
            response.EnsureSuccessStatusCode();
            await using var stream = await response.Content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
            var payload = await JsonSerializer.DeserializeAsync<SearchResponse>(stream, SnakeCaseJson.Options, cancellationToken).ConfigureAwait(false);
            if (payload?.Results is null) return Array.Empty<GeocodingMatch>();

            return payload.Results
                .Select(r => new GeocodingMatch(
                    CanonicalName: BuildCanonicalName(r),
                    Latitude: r.Latitude,
                    Longitude: r.Longitude,
                    Country: r.Country,
                    Admin1: r.Admin1))
                .ToArray();
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Geocoding search failed for query={Query}", trimmed);
            return null;
        }
    }

    private static string BuildCanonicalName(GeocodingResult r)
    {
        var parts = new List<string> { r.Name };
        if (!string.IsNullOrEmpty(r.Admin1) && !string.Equals(r.Admin1, r.Name, StringComparison.OrdinalIgnoreCase))
            parts.Add(r.Admin1);
        if (!string.IsNullOrEmpty(r.Country))
            parts.Add(r.Country);
        return string.Join(", ", parts);
    }

    private sealed record SearchResponse(
        [property: JsonPropertyName("results")] GeocodingResult[]? Results);

    private sealed record GeocodingResult(
        [property: JsonPropertyName("name")] string Name,
        [property: JsonPropertyName("latitude")] double Latitude,
        [property: JsonPropertyName("longitude")] double Longitude,
        [property: JsonPropertyName("country")] string? Country,
        [property: JsonPropertyName("admin1")] string? Admin1);
}

public static class OpenMeteoGeocodingProviderRegistration
{
    public static IServiceCollection AddOpenMeteoGeocoding(this IServiceCollection services)
    {
        services.AddHttpClient(OpenMeteoGeocodingProvider.HttpClientName, c =>
            c.BaseAddress = new Uri("https://geocoding-api.open-meteo.com/"));
        services.AddSingleton<IGeocodingProvider, OpenMeteoGeocodingProvider>();
        return services;
    }
}
