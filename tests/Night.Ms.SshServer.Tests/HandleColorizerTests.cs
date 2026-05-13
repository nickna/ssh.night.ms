using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class HandleColorizerTests
{
    [Fact]
    public void Same_handle_returns_same_color()
    {
        var a = HandleColorizer.ColorFor("alice");
        var b = HandleColorizer.ColorFor("alice");
        Assert.Equal(a, b);
    }

    [Fact]
    public void Case_differences_are_collapsed()
    {
        // "Alice" and "alice" should share a color so display capitalization quirks don't
        // change a user's visual identity in chat.
        var a = HandleColorizer.ColorFor("Alice");
        var b = HandleColorizer.ColorFor("alice");
        var c = HandleColorizer.ColorFor("ALICE");
        Assert.Equal(a, b);
        Assert.Equal(b, c);
    }

    [Fact]
    public void Different_handles_usually_get_different_colors()
    {
        // 16-color palette means collisions are expected for some pairs (~5% pairwise), but
        // a small representative set should map to at least two distinct colors.
        var handles = new[] { "alice", "bob", "carol", "dave", "eve", "frank", "grace", "henry" };
        var colors = handles.Select(HandleColorizer.ColorFor).Distinct().ToList();
        Assert.True(colors.Count >= 2, $"expected at least 2 distinct colors across 8 handles, got {colors.Count}");
    }

    [Fact]
    public void Empty_handle_returns_default_foreground()
    {
        // No handle to hash — fall back to the BBS default so we don't divide-by-zero or
        // index a negative palette entry.
        var color = HandleColorizer.ColorFor(string.Empty);
        Assert.Equal(170, color.R);
        Assert.Equal(170, color.G);
        Assert.Equal(170, color.B);
    }
}
