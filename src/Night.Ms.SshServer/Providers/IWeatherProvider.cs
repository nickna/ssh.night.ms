namespace Night.Ms.SshServer.Providers;

public sealed record WeatherSnapshot(
    string LocationLabel,
    double LatitudeDegrees,
    double LongitudeDegrees,
    double TemperatureCelsius,
    double TemperatureFahrenheit,
    string Conditions,
    DateTimeOffset FetchedAt);

public interface IWeatherProvider
{
    // Returns current weather for the given coordinates, or for the provider's configured
    // default (env-var fallback) when all of latitude/longitude/label are null. Null on
    // upstream failure. Implementations cache per coordinate so multiple sessions sharing a
    // location only hit the upstream API once per cache window.
    Task<WeatherSnapshot?> GetCurrentAsync(
        double? latitude = null,
        double? longitude = null,
        string? label = null,
        CancellationToken cancellationToken = default);
}
