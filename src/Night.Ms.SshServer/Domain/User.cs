namespace Night.Ms.SshServer.Domain;

public sealed class User
{
    public long Id { get; set; }
    public required string Handle { get; set; }
    // Optional. Populated for users who signed up via SSO (Google/Microsoft) or who add an
    // email later from the web profile. Unique citext so a verified-email match across
    // providers can auto-link to the same row. SSH-only users have null Email until they
    // link an SSO credential.
    public string? Email { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? LastSeenAt { get; set; }
    public bool IsSysop { get; set; }
    public bool IsBanned { get; set; }

    // Argon2id-hashed password for SSH (and future web-form) auth. Nullable so existing
    // key-only users keep working; null means "no password set, can only log in via a
    // registered SSH key or an external OIDC provider". PasswordAlgo carries the full
    // parameter string ("argon2id:m=65536,t=3,p=1") so the hasher can detect parameter
    // drift and rehash on next successful login if NeedsRehash returns true.
    public byte[]? PasswordHash { get; set; }
    public string? PasswordAlgo { get; set; }
    public DateTimeOffset? PasswordUpdatedAt { get; set; }

    // Public profile fields, all optional — shown by /finger and editable on the profile screen.
    public string? Bio { get; set; }
    public string? Location { get; set; }
    public string? RealName { get; set; }

    // When the user last uploaded (or replaced) their profile picture. Null = no upload,
    // in which case the rendering layer falls back to a procedural identicon. Doubles as
    // an ETag basis for the web /u/{handle}/avatar endpoint and as a cache key for the
    // in-memory CellGrid cache that the TUI uses.
    public DateTimeOffset? ProfilePictureUpdatedAt { get; set; }

    // Resolved geocoded location, populated when Location validates against the geocoder.
    // Drives per-user weather. LocationCanonical is what the geocoder echoed back; the user's
    // typed Location is kept verbatim for display.
    public double? LocationLatitude { get; set; }
    public double? LocationLongitude { get; set; }
    public string? LocationCanonical { get; set; }
    public LocationSource LocationSource { get; set; }

    // Display preferences. Defaults match the neutral globals applied to every existing row
    // by the AddUserDisplayPreferences migration: UTC, °C, 24-hour, ISO date.
    public string TimeZoneId { get; set; } = "UTC";
    public TemperatureUnit TemperatureUnit { get; set; }
    public ClockFormat ClockFormat { get; set; }
    public DateFormat DateFormat { get; set; }

    // When true, BbsSessionRunner skips the key-adoption modal regardless of whether the
    // offered fingerprint has been per-key dismissed. Independent of the Redis-backed
    // per-key dismissal store — this is the "stop asking me, for any key" toggle.
    public bool SuppressKeyAdoptionPrompts { get; set; }

    public List<IdentityCredential> Credentials { get; set; } = [];
    public List<UserSavedLocation> SavedLocations { get; set; } = [];
    public List<UserWatchlistItem> Watchlist { get; set; } = [];
}
