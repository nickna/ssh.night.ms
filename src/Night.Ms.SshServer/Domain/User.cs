namespace Night.Ms.SshServer.Domain;

public sealed class User
{
    public long Id { get; set; }
    public required string Handle { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? LastSeenAt { get; set; }
    public bool IsSysop { get; set; }
    public bool IsBanned { get; set; }

    // Public profile fields, all optional — shown by /finger and editable on the profile screen.
    public string? Bio { get; set; }
    public string? Location { get; set; }
    public string? RealName { get; set; }

    // Display preferences. Defaults match the neutral globals applied to every existing row
    // by the AddUserDisplayPreferences migration: UTC, °C, 24-hour, ISO date.
    public string TimeZoneId { get; set; } = "UTC";
    public TemperatureUnit TemperatureUnit { get; set; }
    public ClockFormat ClockFormat { get; set; }
    public DateFormat DateFormat { get; set; }

    public List<SshKey> Keys { get; set; } = [];
}
