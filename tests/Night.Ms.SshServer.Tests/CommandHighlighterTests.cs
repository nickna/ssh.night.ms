using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class CommandHighlighterTests
{
    private static readonly ArtColor Cyan       = new(0x6C, 0xC0, 0xFF);
    private static readonly ArtColor Yellow     = new(0xFF, 0xC8, 0x4C);
    private static readonly ArtColor Green      = new(0x9F, 0xE5, 0x9F);
    private static readonly ArtColor ErrorRed   = new(0xFF, 0x70, 0x70);

    private static ChatRun First(ChatLine line) => line.Runs[0];

    private static ChatRun? FindRun(ChatLine line, string text)
        => line.Runs.FirstOrDefault(r => r.Text == text);

    [Fact]
    public void Known_verb_paints_cyan_bold()
    {
        var line = CommandHighlighter.Highlight("/help", "nick");
        Assert.Equal("/help", First(line).Text);
        Assert.Equal(Cyan, First(line).Foreground);
        Assert.True(First(line).Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Unknown_verb_paints_red_and_rest_plain()
    {
        var line = CommandHighlighter.Highlight("/nope arg1 arg2", "nick");
        Assert.Equal("/nope", First(line).Text);
        Assert.Equal(ErrorRed, First(line).Foreground);
        // Remainder stays plain so the row doesn't read all-error.
        var rest = string.Concat(line.Runs.Skip(1).Select(r => r.Text));
        Assert.Equal(" arg1 arg2", rest);
        Assert.Equal(ArtColor.DefaultForeground, line.Runs[1].Foreground);
    }

    [Fact]
    public void Verb_with_trailing_space_emits_whitespace_as_plain_run()
    {
        var line = CommandHighlighter.Highlight("/help ", "nick");
        Assert.Equal("/help", line.Runs[0].Text);
        Assert.Equal(" ", line.Runs[1].Text);
        Assert.Equal(ArtColor.DefaultForeground, line.Runs[1].Foreground);
    }

    [Fact]
    public void Channel_arg_paints_cyan_when_hash_prefixed()
    {
        var line = CommandHighlighter.Highlight("/join #random", "nick");
        var channel = FindRun(line, "#random");
        Assert.NotNull(channel);
        Assert.Equal(Cyan, channel!.Value.Foreground);
        Assert.True(channel.Value.Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Channel_arg_paints_red_when_missing_hash()
    {
        var line = CommandHighlighter.Highlight("/join random", "nick");
        var arg = FindRun(line, "random");
        Assert.NotNull(arg);
        Assert.Equal(ErrorRed, arg!.Value.Foreground);
    }

    [Fact]
    public void Handle_arg_paints_cyan_for_valid_handles()
    {
        var line = CommandHighlighter.Highlight("/dm alice-1", "nick");
        var handle = FindRun(line, "alice-1");
        Assert.NotNull(handle);
        Assert.Equal(Cyan, handle!.Value.Foreground);
    }

    [Fact]
    public void Handle_arg_paints_red_for_illegal_chars()
    {
        var line = CommandHighlighter.Highlight("/finger alice!", "nick");
        var handle = FindRun(line, "alice!");
        Assert.NotNull(handle);
        Assert.Equal(ErrorRed, handle!.Value.Foreground);
    }

    [Fact]
    public void Integer_arg_paints_yellow_when_positive()
    {
        var line = CommandHighlighter.Highlight("/pin 3", "nick");
        var n = FindRun(line, "3");
        Assert.NotNull(n);
        Assert.Equal(Yellow, n!.Value.Foreground);
    }

    [Theory]
    [InlineData("0")]
    [InlineData("-1")]
    [InlineData("abc")]
    [InlineData("3x")]
    public void Integer_arg_paints_red_when_invalid(string token)
    {
        var line = CommandHighlighter.Highlight($"/pin {token}", "nick");
        var n = FindRun(line, token);
        Assert.NotNull(n);
        Assert.Equal(ErrorRed, n!.Value.Foreground);
    }

    [Fact]
    public void React_paints_integer_and_emoji_separately()
    {
        var line = CommandHighlighter.Highlight("/react 2 :+1:", "nick");
        var n = FindRun(line, "2");
        var emoji = FindRun(line, ":+1:");
        Assert.NotNull(n);
        Assert.NotNull(emoji);
        Assert.Equal(Yellow, n!.Value.Foreground);
        Assert.Equal(Green, emoji!.Value.Foreground);
    }

    [Fact]
    public void React_accepts_raw_emoji_glyph()
    {
        var line = CommandHighlighter.Highlight("/react 1 👍", "nick");
        var emoji = FindRun(line, "👍");
        Assert.NotNull(emoji);
        Assert.Equal(Green, emoji!.Value.Foreground);
    }

    [Fact]
    public void React_rejects_ascii_word_as_emoji()
    {
        var line = CommandHighlighter.Highlight("/react 1 thumbs", "nick");
        var emoji = FindRun(line, "thumbs");
        Assert.NotNull(emoji);
        Assert.Equal(ErrorRed, emoji!.Value.Foreground);
    }

    [Fact]
    public void Body_arg_runs_through_message_renderer_for_inline_format()
    {
        // /me action with *bold* — the bold marker should turn into a styled run via
        // MessageRenderer.PreviewBody, and the * markers themselves are stripped.
        var line = CommandHighlighter.Highlight("/me waves *hard*", "nick");
        var bold = FindRun(line, "hard");
        Assert.NotNull(bold);
        Assert.True(bold!.Value.Style.HasFlag(ArtStyle.Bold));
        // The literal asterisks shouldn't appear as their own runs.
        Assert.DoesNotContain(line.Runs, r => r.Text == "*");
    }

    [Fact]
    public void Edit_paints_n_then_inline_formats_body()
    {
        var line = CommandHighlighter.Highlight("/edit 5 _quietly_", "nick");
        var n = FindRun(line, "5");
        Assert.NotNull(n);
        Assert.Equal(Yellow, n!.Value.Foreground);
        var italic = FindRun(line, "quietly");
        Assert.NotNull(italic);
        Assert.True(italic!.Value.Style.HasFlag(ArtStyle.Italic));
    }

    [Fact]
    public void Partial_input_with_no_args_yet_paints_just_verb()
    {
        var line = CommandHighlighter.Highlight("/react", "nick");
        Assert.Single(line.Runs);
        Assert.Equal("/react", line.Runs[0].Text);
        Assert.Equal(Cyan, line.Runs[0].Foreground);
    }

    [Fact]
    public void Verb_lookup_is_case_insensitive()
    {
        var line = CommandHighlighter.Highlight("/HELP", "nick");
        Assert.Equal(Cyan, First(line).Foreground);
    }

    [Fact]
    public void Search_term_spans_rest_of_line_as_single_run()
    {
        var line = CommandHighlighter.Highlight("/search hello world", "nick");
        // Verb + one space + remainder as a single term run.
        var term = FindRun(line, "hello world");
        Assert.NotNull(term);
    }
}
