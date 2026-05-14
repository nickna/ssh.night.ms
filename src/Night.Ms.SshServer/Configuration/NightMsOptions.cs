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
    public int? HttpPort { get; init; }
    public string? BootstrapSysopHandle { get; init; }
    public string? LoginArtPath { get; init; }
    public string? ArtGalleryPath { get; init; }
    public string? ProfilePictureDirectory { get; init; }
    public string? WeatherLabel { get; init; }
    public double? WeatherLatitude { get; init; }
    public double? WeatherLongitude { get; init; }

    // Optional. When all four (GoogleClientId, GoogleClientSecret, MicrosoftClientId,
    // MicrosoftClientSecret) are set, the web auth handlers register; otherwise the
    // corresponding "Sign in with X" button is hidden. WebBaseUrl + SshHost are display-only
    // hints — the auth path derives callback URLs from request scheme/host (via forwarded
    // headers), so this never affects auth. They're split because the web origin and the
    // SSH origin can be different aliases of the same host (e.g. https://k.night.ms vs
    // ssh.night.ms). SshPortPublic is the externally-visible SSH port — it can differ from
    // the listener port (BBS_SSH_PORT) when a docker port mapping or NAT rule is in front.
    public string? GoogleClientId { get; init; }
    public string? GoogleClientSecret { get; init; }
    public string? MicrosoftClientId { get; init; }
    public string? MicrosoftClientSecret { get; init; }
    public string? WebBaseUrl { get; init; }
    public string? SshHost { get; init; }
    public int? SshPortPublic { get; init; }

    public bool IsGoogleConfigured =>
        !string.IsNullOrWhiteSpace(GoogleClientId) && !string.IsNullOrWhiteSpace(GoogleClientSecret);
    public bool IsMicrosoftConfigured =>
        !string.IsNullOrWhiteSpace(MicrosoftClientId) && !string.IsNullOrWhiteSpace(MicrosoftClientSecret);

    public static NightMsOptions FromConfiguration(IConfiguration cfg) => new()
    {
        HostKeyDirectory     = First(cfg, "NIGHTMS_HOST_KEY_DIR", "HostKeyDirectory"),
        SshPort              = ParseInt(cfg["BBS_SSH_PORT"]),
        HttpPort             = ParseInt(cfg["BBS_HTTP_PORT"]),
        BootstrapSysopHandle = TrimOrNull(cfg["NIGHTMS_BOOTSTRAP_SYSOP_HANDLE"]),
        LoginArtPath         = First(cfg, "NIGHTMS_LOGIN_ART_PATH", "LoginArt:Path"),
        ArtGalleryPath       = First(cfg, "NIGHTMS_ART_DIR", "ArtGallery:Path"),
        ProfilePictureDirectory = First(cfg, "NIGHTMS_PFP_DIR", "ProfilePictures:Path"),
        WeatherLabel         = NullIfEmpty(cfg["NIGHTMS_WEATHER_LABEL"]),
        WeatherLatitude      = ParseDouble(cfg["NIGHTMS_WEATHER_LAT"]),
        WeatherLongitude     = ParseDouble(cfg["NIGHTMS_WEATHER_LON"]),
        GoogleClientId       = First(cfg, "NIGHTMS_GOOGLE_CLIENT_ID", "Authentication:Google:ClientId"),
        GoogleClientSecret   = First(cfg, "NIGHTMS_GOOGLE_CLIENT_SECRET", "Authentication:Google:ClientSecret"),
        MicrosoftClientId    = First(cfg, "NIGHTMS_MICROSOFT_CLIENT_ID", "Authentication:Microsoft:ClientId"),
        MicrosoftClientSecret= First(cfg, "NIGHTMS_MICROSOFT_CLIENT_SECRET", "Authentication:Microsoft:ClientSecret"),
        WebBaseUrl           = First(cfg, "NIGHTMS_WEB_BASE_URL", "WebBaseUrl"),
        SshHost              = First(cfg, "NIGHTMS_SSH_HOST", "SshHost"),
        SshPortPublic        = ParseInt(cfg["NIGHTMS_SSH_PORT_PUBLIC"]),
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
