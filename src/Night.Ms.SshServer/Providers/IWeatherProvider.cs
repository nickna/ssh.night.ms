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
    // Returns the current weather for the provider's configured location, or null if the
    // upstream call failed. Implementations are expected to cache results so the UI can
    // call this on every screen open without hammering the external API.
    Task<WeatherSnapshot?> GetCurrentAsync(CancellationToken cancellationToken = default);
}
