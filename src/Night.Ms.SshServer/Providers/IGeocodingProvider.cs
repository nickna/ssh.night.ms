namespace Night.Ms.SshServer.Providers;

// A single match returned by a forward-geocoding query (free-text → coords). CanonicalName
// is the geocoder's normalized rendering, suitable for display ("San Francisco, California,
// United States"). The score lets callers prefer the more populous/admin-aligned hit when
// the query is ambiguous.
public sealed record GeocodingMatch(
    string CanonicalName,
    double Latitude,
    double Longitude,
    string? Country,
    string? Admin1);

public interface IGeocodingProvider
{
    // Resolves a free-text location query into ranked candidates. Returns an empty list when
    // the query has zero matches (caller treats this as a validation failure). Returns null
    // on transient upstream failure so the caller can distinguish "definitely no match" from
    // "we couldn't ask" and pick a different fallback (e.g., IP-based guess).
    Task<IReadOnlyList<GeocodingMatch>?> SearchAsync(string query, CancellationToken cancellationToken = default);

    // Reverse geocoding: turn coordinates into a canonical display name. Used after an IP
    // lookup returns lat/lon so the "Use detected location" prompt can show a city name.
    Task<GeocodingMatch?> ReverseAsync(double latitude, double longitude, CancellationToken cancellationToken = default);
}
