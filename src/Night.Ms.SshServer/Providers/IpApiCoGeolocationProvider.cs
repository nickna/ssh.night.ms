using System.Net;
using System.Net.Sockets;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Night.Ms.SshServer.Providers;

// IP geolocation backed by https://ipapi.co — no API key, HTTPS, 1000 req/day on the free
// tier. Returns city-level lat/lon and a human-readable region/country. We treat any
// upstream failure as "no fix available" rather than surfacing the error, since the only
// caller (profile-save fallback) has graceful behavior when the suggestion is missing.
public sealed class IpApiCoGeolocationProvider(IHttpClientFactory httpClientFactory, ILogger<IpApiCoGeolocationProvider> logger)
    : IIpGeolocationProvider
{
    public const string HttpClientName = "ipapi.co";

    public async Task<IpGeolocationResult?> LookupAsync(IPAddress ip, CancellationToken cancellationToken = default)
    {
        if (ip is null || !IsPubliclyRouteable(ip))
            return null;

        var uri = $"{ip}/json/";
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            using var response = await http.GetAsync(uri, cancellationToken).ConfigureAwait(false);
            if (!response.IsSuccessStatusCode)
            {
                logger.LogDebug("ipapi.co returned {Status} for {Ip}", response.StatusCode, ip);
                return null;
            }
            await using var stream = await response.Content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
            var payload = await JsonSerializer.DeserializeAsync<IpApiResponse>(stream, SnakeCaseJson.Options, cancellationToken).ConfigureAwait(false);
            // ipapi.co returns HTTP 200 even when the IP is reserved/private, but flags it with
            // an `error` field. Detect that explicitly so we don't surface bogus 0,0 coords.
            if (payload is null || payload.Error == true || payload.Latitude is null || payload.Longitude is null)
                return null;

            return new IpGeolocationResult(
                DisplayName: BuildDisplayName(payload),
                Latitude: payload.Latitude.Value,
                Longitude: payload.Longitude.Value,
                City: payload.City,
                Region: payload.Region,
                Country: payload.CountryName);
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "ipapi.co lookup failed for {Ip}", ip);
            return null;
        }
    }

    // RFC 1918 + loopback + link-local + multicast + IPv6 equivalents. We don't try to be
    // exhaustive about every reserved range — anything we miss just produces a null lookup,
    // which is the same outcome as a public IP the geocoder can't place.
    private static bool IsPubliclyRouteable(IPAddress ip)
    {
        if (IPAddress.IsLoopback(ip)) return false;
        if (ip.AddressFamily == AddressFamily.InterNetwork)
        {
            var bytes = ip.GetAddressBytes();
            if (bytes[0] == 10) return false;                            // 10.0.0.0/8
            if (bytes[0] == 172 && bytes[1] >= 16 && bytes[1] <= 31) return false; // 172.16.0.0/12
            if (bytes[0] == 192 && bytes[1] == 168) return false;        // 192.168.0.0/16
            if (bytes[0] == 169 && bytes[1] == 254) return false;        // 169.254.0.0/16 link-local
            if (bytes[0] >= 224) return false;                           // multicast + reserved
        }
        else if (ip.AddressFamily == AddressFamily.InterNetworkV6)
        {
            if (ip.IsIPv6LinkLocal || ip.IsIPv6SiteLocal || ip.IsIPv6Multicast) return false;
            var bytes = ip.GetAddressBytes();
            // fc00::/7 unique-local
            if ((bytes[0] & 0xfe) == 0xfc) return false;
        }
        return true;
    }

    private static string BuildDisplayName(IpApiResponse p)
    {
        var parts = new List<string>();
        if (!string.IsNullOrEmpty(p.City)) parts.Add(p.City);
        if (!string.IsNullOrEmpty(p.Region)) parts.Add(p.Region);
        if (!string.IsNullOrEmpty(p.CountryName)) parts.Add(p.CountryName);
        return parts.Count > 0 ? string.Join(", ", parts) : "Unknown location";
    }

    private sealed record IpApiResponse(
        [property: JsonPropertyName("latitude")] double? Latitude,
        [property: JsonPropertyName("longitude")] double? Longitude,
        [property: JsonPropertyName("city")] string? City,
        [property: JsonPropertyName("region")] string? Region,
        [property: JsonPropertyName("country_name")] string? CountryName,
        [property: JsonPropertyName("error")] bool? Error);
}

public static class IpApiCoGeolocationProviderRegistration
{
    public static IServiceCollection AddIpApiCoGeolocation(this IServiceCollection services)
    {
        services.AddHttpClient(IpApiCoGeolocationProvider.HttpClientName, c =>
            c.BaseAddress = new Uri("https://ipapi.co/"));
        services.AddSingleton<IIpGeolocationProvider, IpApiCoGeolocationProvider>();
        return services;
    }
}
