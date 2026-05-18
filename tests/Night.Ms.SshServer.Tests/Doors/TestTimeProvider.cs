namespace Night.Ms.SshServer.Tests.Doors;

// Minimal TimeProvider fake — we don't pull in Microsoft.Extensions.Time.Testing because
// the door-game tests only need to advance the clock by whole days for the daily-reset
// boundary. SetUtcNow lets a test pin "now" to a specific UTC instant.
internal sealed class TestTimeProvider(DateTimeOffset start) : TimeProvider
{
    private DateTimeOffset _now = start;

    public override DateTimeOffset GetUtcNow() => _now;

    public void SetUtcNow(DateTimeOffset value) => _now = value;
    public void Advance(TimeSpan delta) => _now = _now.Add(delta);
}
