namespace Night.Ms.SshServer.Providers;

// Rich forecast payload: current conditions + a short hourly horizon + a week of daily
// summaries. Returned by IWeatherProvider.GetForecastAsync. WeatherSnapshot stays in place
// for the small status-bar / News sidebar use cases that only need "right now"; this record
// is what the dedicated WeatherScreen renders.
public sealed record WeatherForecast(
    string LocationLabel,
    double LatitudeDegrees,
    double LongitudeDegrees,
    string? TimeZone,
    CurrentConditions Current,
    IReadOnlyList<HourlyForecast> Hourly,
    IReadOnlyList<DailyForecast> Daily,
    DateTimeOffset FetchedAt);

public sealed record CurrentConditions(
    double TemperatureCelsius,
    double TemperatureFahrenheit,
    double ApparentTemperatureCelsius,
    double ApparentTemperatureFahrenheit,
    int RelativeHumidityPercent,
    double WindSpeedKph,
    double WindSpeedMph,
    int WindDirectionDegrees,
    double PrecipitationMm,
    double PrecipitationInches,
    int WeatherCode,
    bool IsDay,
    string Conditions);

public sealed record HourlyForecast(
    DateTimeOffset Time,
    double TemperatureCelsius,
    double TemperatureFahrenheit,
    int PrecipitationProbabilityPercent,
    double PrecipitationMm,
    double PrecipitationInches,
    int WeatherCode,
    bool IsDay,
    string Conditions);

public sealed record DailyForecast(
    DateOnly Date,
    DateTimeOffset Sunrise,
    DateTimeOffset Sunset,
    double TemperatureMaxCelsius,
    double TemperatureMaxFahrenheit,
    double TemperatureMinCelsius,
    double TemperatureMinFahrenheit,
    int PrecipitationProbabilityMaxPercent,
    double PrecipitationSumMm,
    double PrecipitationSumInches,
    double UvIndexMax,
    int WeatherCode,
    string Conditions);

// High-level animation buckets. Coarser than WMO codes — many weather codes collapse into
// the same visual scene (e.g. drizzle / light rain share an animation). Time-of-day variants
// exist only for conditions where the sky changes visibly between day and night (clear /
// partly cloudy). Heavily-occluded conditions (rain, snow, fog, thunderstorm) reuse a
// single scene across day and night.
public enum WeatherCondition
{
    Unknown = 0,
    ClearDay,
    ClearNight,
    PartlyCloudyDay,
    PartlyCloudyNight,
    Cloudy,
    Fog,
    Drizzle,
    Rain,
    Snow,
    Thunderstorm,
}
