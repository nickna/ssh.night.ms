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
    public string? LobbyIconsPath { get; init; }
    public string? ProfilePictureDirectory { get; init; }
    public string? WeatherArtPath { get; init; }
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

    // Plaintext password set on first run for the sysop handle. Hashed once via
    // IPasswordHasher.Hash on boot in SysopBootstrap; never re-applied if the user already
    // has a password_hash so an admin who changed their password via UI isn't reset on
    // container restart.
    public string? BootstrapSysopPassword { get; init; }

    public PasswordHashingOptions PasswordHashing { get; init; } = new();
    public LoginLockoutOptions Lockout { get; init; } = new();
    public KeyAdoptionOptions KeyAdoption { get; init; } = new();

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
        LobbyIconsPath       = First(cfg, "NIGHTMS_LOBBY_ICONS_DIR", "LobbyIcons:Path"),
        ProfilePictureDirectory = First(cfg, "NIGHTMS_PFP_DIR", "ProfilePictures:Path"),
        WeatherArtPath       = First(cfg, "NIGHTMS_WEATHER_ART_DIR", "WeatherArt:Path"),
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
        BootstrapSysopPassword = NullIfEmpty(cfg["NIGHTMS_BOOTSTRAP_SYSOP_PASSWORD"]),
        PasswordHashing      = PasswordHashingOptions.FromConfiguration(cfg),
        Lockout              = LoginLockoutOptions.FromConfiguration(cfg),
        KeyAdoption          = KeyAdoptionOptions.FromConfiguration(cfg),
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
    internal static int ParseIntOr(string? s, int fallback) => int.TryParse(s, out var v) ? v : fallback;
}

// Argon2id parameters. Defaults follow OWASP 2024 guidance for interactive logins: 64 MiB
// memory, 3 iterations, 1 lane. Bump MemoryKb / Iterations for slower-but-stronger hashing if
// you run on beefier hardware. The verify path keeps a precomputed throwaway hash with these
// same params so unknown-user verifies are timing-equivalent to real verifies.
public sealed class PasswordHashingOptions
{
    public int MemoryKb { get; init; } = 65536;        // 64 MiB
    public int Iterations { get; init; } = 3;
    public int Parallelism { get; init; } = 1;
    public int SaltBytes { get; init; } = 16;
    public int HashBytes { get; init; } = 32;
    public int MinPasswordLength { get; init; } = 10;

    public static PasswordHashingOptions FromConfiguration(IConfiguration cfg) => new()
    {
        MemoryKb = NightMsOptions.ParseIntOr(cfg["NIGHTMS_ARGON2_MEM_KB"], 65536),
        Iterations = NightMsOptions.ParseIntOr(cfg["NIGHTMS_ARGON2_ITERATIONS"], 3),
        Parallelism = NightMsOptions.ParseIntOr(cfg["NIGHTMS_ARGON2_PARALLELISM"], 1),
        SaltBytes = NightMsOptions.ParseIntOr(cfg["NIGHTMS_ARGON2_SALT_BYTES"], 16),
        HashBytes = NightMsOptions.ParseIntOr(cfg["NIGHTMS_ARGON2_HASH_BYTES"], 32),
        MinPasswordLength = NightMsOptions.ParseIntOr(cfg["NIGHTMS_PASSWORD_MIN_LENGTH"], 10),
    };
}

// Sliding-window lockout for SSH password auth. Failures are counted in Redis with a TTL
// equal to WindowSeconds; reaching FailureThreshold within the window sets a lockout key
// with TTL = LockoutSeconds. Counted per-handle AND per-source-IP separately — the IP
// thresholds are deliberately higher so a shared NAT doesn't lock out an entire household
// after one user mistypes a password.
public sealed class LoginLockoutOptions
{
    public int HandleFailureThreshold { get; init; } = 5;
    public int IpFailureThreshold { get; init; } = 20;
    public int WindowSeconds { get; init; } = 900;     // 15 min
    public int LockoutSeconds { get; init; } = 900;    // 15 min

    public static LoginLockoutOptions FromConfiguration(IConfiguration cfg) => new()
    {
        HandleFailureThreshold = NightMsOptions.ParseIntOr(cfg["NIGHTMS_LOCKOUT_HANDLE_THRESHOLD"], 5),
        IpFailureThreshold = NightMsOptions.ParseIntOr(cfg["NIGHTMS_LOCKOUT_IP_THRESHOLD"], 20),
        WindowSeconds = NightMsOptions.ParseIntOr(cfg["NIGHTMS_LOCKOUT_WINDOW_SECONDS"], 900),
        LockoutSeconds = NightMsOptions.ParseIntOr(cfg["NIGHTMS_LOCKOUT_SECONDS"], 900),
    };
}

// How long a user's "Never for this key" dismissal of the adopt-key prompt sticks. Lives in
// Redis under dismissed:{userId}:{fingerprint}. Default 90 days — long enough not to be
// annoying, short enough that a user who deliberately rotates keys gets re-prompted.
public sealed class KeyAdoptionOptions
{
    public int DismissalTtlDays { get; init; } = 90;

    public static KeyAdoptionOptions FromConfiguration(IConfiguration cfg) => new()
    {
        DismissalTtlDays = NightMsOptions.ParseIntOr(cfg["NIGHTMS_ADOPT_KEY_DISMISS_DAYS"], 90),
    };
}
