using System.Net;

namespace Night.Ms.SshServer.Providers;

public sealed record IpGeolocationResult(
    string DisplayName,
    double Latitude,
    double Longitude,
    string? City,
    string? Region,
    string? Country);

public interface IIpGeolocationProvider
{
    // Returns a best-effort city-level fix for a public IP, or null when the IP is private,
    // loopback, unrouteable, or the upstream call fails. Callers should treat null as
    // "no IP-based suggestion available" — the UI should fall through without an error.
    Task<IpGeolocationResult?> LookupAsync(IPAddress ip, CancellationToken cancellationToken = default);
}
