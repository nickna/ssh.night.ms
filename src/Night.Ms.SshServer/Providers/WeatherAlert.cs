namespace Night.Ms.SshServer.Providers;

public enum AlertSeverity { Unknown, Minor, Moderate, Severe, Extreme }

public sealed record WeatherAlert(
    string Id,
    string Event,
    AlertSeverity Severity,
    string Headline,
    string Description,
    string AreaDescription,
    DateTimeOffset Effective,
    DateTimeOffset Expires);
