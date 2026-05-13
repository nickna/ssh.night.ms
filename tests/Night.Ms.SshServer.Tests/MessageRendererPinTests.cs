using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class MessageRendererPinTests
{
    [Fact]
    public void Pinned_message_prefixes_yellow_star()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "important", "nick", pinned: true);

        // First run should be the pin marker.
        Assert.Equal("★ ", line.Runs[0].Text);
        Assert.True(line.Runs[0].Style.HasFlag(ArtStyle.Bold));
        Assert.True(line.Runs[0].Foreground.R > 0xC0); // bright (yellow-ish)
    }

    [Fact]
    public void Unpinned_message_has_no_marker()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "important", "nick");
        Assert.DoesNotContain(line.Runs, r => r.Text.StartsWith("★"));
    }

    [Fact]
    public void Pinned_and_edited_compose()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "fixed", "nick", edited: true, pinned: true);
        Assert.Equal("★ ", line.Runs[0].Text);
        Assert.Contains(line.Runs, r => r.Text.Contains("(edited)"));
    }
}
