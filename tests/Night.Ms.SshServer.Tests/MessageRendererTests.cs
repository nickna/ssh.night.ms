using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class MessageRendererTests
{
    private static string Body(ChatLine line)
    {
        // Concatenate body runs (skipping the chrome prefix: "[clock] ", "handle", ": ").
        return string.Concat(line.Runs.Skip(3).Select(r => r.Text));
    }

    [Fact]
    public void Plain_message_emits_chrome_then_body()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "hello world", selfHandle: "nick");

        Assert.Equal("[12:34] ", line.Runs[0].Text);
        Assert.Equal("alice", line.Runs[1].Text);
        Assert.Equal(": ", line.Runs[2].Text);
        Assert.Equal("hello world", Body(line));
        Assert.False(line.SelfMentioned);
    }

    [Fact]
    public void Sender_handle_is_colored_per_hash_and_bold()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "x", "nick");
        Assert.Equal(HandleColorizer.ColorFor("alice"), line.Runs[1].Foreground);
        Assert.True(line.Runs[1].Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Self_mention_sets_flag_and_paints_in_self_color()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "hey @nick can you look at this?", "nick");

        Assert.True(line.SelfMentioned);
        var mention = line.Runs.First(r => r.Text == "@nick");
        Assert.Equal(0xFF, mention.Foreground.R);
        Assert.Equal(0xD7, mention.Foreground.G);
        Assert.True(mention.Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Other_mention_paints_cyan_without_self_flag()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "hey @bob look", "nick");

        Assert.False(line.SelfMentioned);
        var mention = line.Runs.First(r => r.Text == "@bob");
        Assert.True(mention.Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Email_address_does_not_count_as_mention()
    {
        // "user@host" — the @ has a word-char immediately before it, so the lookbehind
        // should suppress the mention match.
        var line = MessageRenderer.RenderMessage("12:34", "alice", "mail me at nick@example.com", "nick");
        Assert.False(line.SelfMentioned);
        Assert.DoesNotContain(line.Runs, r => r.Text == "@example");
    }

    [Fact]
    public void Bold_marker_strips_asterisks_and_applies_style()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "this is *important* stuff", "nick");
        var bold = line.Runs.First(r => r.Text == "important");
        Assert.True(bold.Style.HasFlag(ArtStyle.Bold));
        Assert.Equal(0xFF, bold.Foreground.R); // white
    }

    [Fact]
    public void Italic_marker_strips_underscores_and_applies_style()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "saying _maybe_ tonight", "nick");
        var italic = line.Runs.First(r => r.Text == "maybe");
        Assert.True(italic.Style.HasFlag(ArtStyle.Italic));
    }

    [Fact]
    public void Code_marker_strips_backticks_and_paints_green()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "run `dotnet build` first", "nick");
        var code = line.Runs.First(r => r.Text == "dotnet build");
        Assert.Equal(0x9F, code.Foreground.R); // green-ish from CodeFg
    }

    [Fact]
    public void Unmatched_single_marker_is_left_literal()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "5 * 3 = 15", "nick");
        Assert.Equal("5 * 3 = 15", Body(line));
    }

    [Fact]
    public void Empty_body_emits_only_chrome()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "", "nick");
        Assert.Equal(3, line.Runs.Count); // [clock] / handle / ": "
        Assert.Equal(string.Empty, Body(line));
    }

    [Fact]
    public void Emoji_substitution_runs_before_inline_format()
    {
        var line = MessageRenderer.RenderMessage("12:34", "alice", "ship it :rocket:", "nick");
        Assert.Contains("\U0001F680", Body(line));
    }

    [Fact]
    public void Emote_renders_action_with_italic_prefix()
    {
        var line = MessageRenderer.RenderEmote("12:34", "alice", "waves at the lobby", "nick");
        // Chrome run, "* alice " run, then action.
        Assert.True(line.Runs[1].Style.HasFlag(ArtStyle.Italic));
        Assert.True(line.Runs[1].Style.HasFlag(ArtStyle.Bold));
        Assert.Contains("waves at the lobby", line.Runs.Skip(2).Select(r => r.Text));
    }

    [Fact]
    public void System_line_is_single_styled_run()
    {
        var line = MessageRenderer.RenderSystem("--- joined #lobby ---");
        Assert.Single(line.Runs);
        Assert.Equal("--- joined #lobby ---", line.Runs[0].Text);
        Assert.False(line.SelfMentioned);
    }

    [Fact]
    public void System_error_line_is_bold_red()
    {
        var line = MessageRenderer.RenderSystem("[!] something broke", isError: true);
        Assert.True(line.Runs[0].Style.HasFlag(ArtStyle.Bold));
        Assert.Equal(0xFF, line.Runs[0].Foreground.R);
    }

    [Fact]
    public void Raw_line_has_no_formatting_applied()
    {
        // /finger output uses RenderRaw so a fingerprint like "ed25519 _AAAAC..." doesn't
        // get italicized between the underscores.
        var line = MessageRenderer.RenderRaw("ed25519 _AAAAC3NzaC1lZ_DI1NTE5...");
        Assert.Single(line.Runs);
        Assert.Equal(ArtStyle.None, line.Runs[0].Style);
    }
}
