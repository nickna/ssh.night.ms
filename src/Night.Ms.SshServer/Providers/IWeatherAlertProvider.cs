namespace Night.Ms.SshServer.Providers;

public interface IWeatherAlertProvider
{
    Task<IReadOnlyList<WeatherAlert>> GetActiveAlertsAsync(
        double latitude, double longitude, CancellationToken cancellationToken = default);
}
