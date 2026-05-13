using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class MessageRendererEditDeleteTests
{
    [Fact]
    public void Edited_message_appends_italic_marker()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "fixed typo", "nick", edited: true);
        var last = line.Runs[^1];
        Assert.Contains("(edited)", last.Text);
        Assert.True(last.Style.HasFlag(ArtStyle.Italic));
    }

    [Fact]
    public void Unedited_message_has_no_marker()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "fixed typo", "nick");
        Assert.DoesNotContain(line.Runs, r => r.Text.Contains("(edited)"));
    }

    [Fact]
    public void Deleted_renders_tombstone_with_faint_body()
    {
        var line = MessageRenderer.RenderDeleted("12:34", "alice");
        // Chrome, handle, ": ", body
        Assert.Equal(4, line.Runs.Count);
        Assert.Equal("(deleted)", line.Runs[3].Text);
        Assert.True(line.Runs[3].Style.HasFlag(ArtStyle.Italic));
        // Body color should be a mid-gray, distinctly dimmer than the default fg.
        Assert.True(line.Runs[3].Foreground.R < ArtColor.DefaultForeground.R);
    }
}
