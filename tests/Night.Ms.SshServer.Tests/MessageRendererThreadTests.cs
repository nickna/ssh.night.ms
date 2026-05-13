using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class MessageRendererThreadTests
{
    [Fact]
    public void Reply_prefix_appears_after_chrome_with_arrow_and_at_sign()
    {
        var line = MessageRenderer.RenderMessage(
            "12:34", "bob", "yes that works", "nick",
            replyToHandle: "alice");

        // Chrome (clock, handle, ": "), then "↳ @", "alice", " ", then body runs.
        Assert.Contains(line.Runs, r => r.Text == "↳ @");
        Assert.Contains(line.Runs, r => r.Text == "alice");
    }

    [Fact]
    public void Reply_prefix_omitted_when_handle_null()
    {
        var line = MessageRenderer.RenderMessage("12:34", "bob", "regular", "nick");
        Assert.DoesNotContain(line.Runs, r => r.Text.StartsWith("↳"));
    }

    [Fact]
    public void Reply_count_renders_pluralized_badge()
    {
        var line = MessageRenderer.RenderMessage(
            "12:34", "alice", "topic message", "nick",
            replyCount: 3);

        Assert.Contains(line.Runs, r => r.Text == "  [3 replies]");
        Assert.True(line.Runs.Last(r => r.Text.Contains("replies")).Style.HasFlag(ArtStyle.Italic));
    }

    [Fact]
    public void Reply_count_singular_renders_one_reply()
    {
        var line = MessageRenderer.RenderMessage(
            "12:34", "alice", "topic message", "nick",
            replyCount: 1);

        Assert.Contains(line.Runs, r => r.Text == "  [1 reply]");
    }

    [Fact]
    public void Reply_count_zero_renders_nothing()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "topic message", "nick", replyCount: 0);
        Assert.DoesNotContain(line.Runs, r => r.Text.Contains("reply") || r.Text.Contains("replies"));
    }

    [Fact]
    public void Reply_prefix_and_count_compose_with_pin_and_edit()
    {
        // A pinned message that is itself a reply, has been edited, and has 2 children:
        // ★ [12:34] bob: ↳ @alice fixed (edited)  [2 replies]
        var line = MessageRenderer.RenderMessage(
            "12:34", "bob", "fixed", "nick",
            edited: true, pinned: true, replyToHandle: "alice", replyCount: 2);

        Assert.Equal("★ ", line.Runs[0].Text);
        Assert.Contains(line.Runs, r => r.Text == "↳ @");
        Assert.Contains(line.Runs, r => r.Text.Contains("(edited)"));
        Assert.Contains(line.Runs, r => r.Text == "  [2 replies]");
    }
}
