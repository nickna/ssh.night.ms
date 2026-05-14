using System.Globalization;

namespace Night.Ms.SshServer.Configuration;

// Single typed source of truth for the BBS's optional environment-variable / appsettings
// knobs. Replaces the per-consumer `configuration["NIGHTMS_*"] ?? configuration["..."]`
// pattern that had grown to five sites with subtly different fallback rules.
//
// Bound once at startup via FromConfiguration and registered as a singleton. Consumers
// take this instead of IConfiguration so the alias-key fallback lives in one place and a
// new key can't be added in only one of two locations.
//
// Defaults that are domain-specific (NYC weather coords, default art path, listener port
// 2222) intentionally remain at the call site — this object exposes the parsed user input
// and lets each consumer decide what "missing" means.
public sealed class NightMsOptions
{
    public string? HostKeyDirectory { get; init; }
    public int? SshPort { get; init; }
    public string? BootstrapSysopHandle { get; init; }
    public string? LoginArtPath { get; init; }
    public string? ArtGalleryPath { get; init; }
    public string? WeatherLabel { get; init; }
    public double? WeatherLatitude { get; init; }
    public double? WeatherLongitude { get; init; }

    public static NightMsOptions FromConfiguration(IConfiguration cfg) => new()
    {
        HostKeyDirectory     = First(cfg, "NIGHTMS_HOST_KEY_DIR", "HostKeyDirectory"),
        SshPort              = ParseInt(cfg["BBS_SSH_PORT"]),
        BootstrapSysopHandle = TrimOrNull(cfg["NIGHTMS_BOOTSTRAP_SYSOP_HANDLE"]),
        LoginArtPath         = First(cfg, "NIGHTMS_LOGIN_ART_PATH", "LoginArt:Path"),
        ArtGalleryPath       = First(cfg, "NIGHTMS_ART_DIR", "ArtGallery:Path"),
        WeatherLabel         = NullIfEmpty(cfg["NIGHTMS_WEATHER_LABEL"]),
        WeatherLatitude      = ParseDouble(cfg["NIGHTMS_WEATHER_LAT"]),
        WeatherLongitude     = ParseDouble(cfg["NIGHTMS_WEATHER_LON"]),
    };

    private static string? First(IConfiguration cfg, params string[] keys)
    {
        foreach (var k in keys)
        {
            var v = NullIfEmpty(cfg[k]);
            if (v is not null) return v;
        }
        return null;
    }

    private static string? NullIfEmpty(string? s) => string.IsNullOrEmpty(s) ? null : s;
    private static string? TrimOrNull(string? s)
    {
        var t = s?.Trim();
        return string.IsNullOrEmpty(t) ? null : t;
    }

    private static int? ParseInt(string? s) => int.TryParse(s, out var v) ? v : null;
    private static double? ParseDouble(string? s) =>
        double.TryParse(s, NumberStyles.Float, CultureInfo.InvariantCulture, out var v) ? v : null;
}
