namespace Night.Ms.SshServer.Domain;

// A favorite location a user keeps on their weather screen so they can flip between, say,
// "home" and "Tokyo" with one keypress while traveling. Distinct from User.LocationLatitude:
// the User row holds the canonical home; this table holds the picker-style favorites list.
// SortOrder drives the F1..F9 hotkey mapping on WeatherScreen — app code enforces the 1..9
// cap (no DB constraint) so a future "10+ favorites with scrolling" change costs nothing.
public sealed class UserSavedLocation
{
    public long Id { get; set; }
    public long UserId { get; set; }
    public User? User { get; set; }

    // Short, user-chosen display label ("Home", "Tokyo work", "Mom's house"). Distinct from
    // Canonical — the latter is the geocoder's full name; the former is whatever the user
    // wants to see on the picker.
    public required string Label { get; set; }
    public double Latitude { get; set; }
    public double Longitude { get; set; }

    // Geocoder's canonical rendering. Used for the GetForecastAsync call's `label` argument
    // so the screen header reads "Tokyo, Tokyo, Japan" rather than the user's nickname.
    public string? Canonical { get; set; }

    public int SortOrder { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
}
