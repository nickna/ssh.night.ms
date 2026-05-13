using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class EmojiTableTests
{
    [Fact]
    public void Substitute_replaces_known_shortcode()
    {
        var result = EmojiTable.Substitute("hello :wave:");
        Assert.Equal("hello \U0001F44B", result);
    }

    [Fact]
    public void Substitute_replaces_multiple_in_one_message()
    {
        var result = EmojiTable.Substitute(":fire: :100: :rocket:");
        Assert.Equal("\U0001F525 \U0001F4AF \U0001F680", result);
    }

    [Fact]
    public void Substitute_is_case_insensitive()
    {
        // ":SMILE:" should resolve the same as ":smile:".
        Assert.Equal("\U0001F600", EmojiTable.Substitute(":SMILE:"));
    }

    [Fact]
    public void Unknown_shortcode_passes_through_unchanged()
    {
        var result = EmojiTable.Substitute("look :notarealemoji: now");
        Assert.Equal("look :notarealemoji: now", result);
    }

    [Fact]
    public void Stray_colon_is_left_literal()
    {
        var result = EmojiTable.Substitute("port :22 is open");
        Assert.Equal("port :22 is open", result);
    }

    [Fact]
    public void Empty_input_returns_empty()
    {
        Assert.Equal(string.Empty, EmojiTable.Substitute(string.Empty));
    }

    [Fact]
    public void Plus_minus_aliases_resolve()
    {
        Assert.Equal("\U0001F44D", EmojiTable.Substitute(":+1:"));
        Assert.Equal("\U0001F44E", EmojiTable.Substitute(":-1:"));
    }

    [Fact]
    public void Unmatched_open_colon_does_not_consume_rest_of_string()
    {
        // Lookahead is capped at 32 chars so a stray opening colon doesn't eat a long tail.
        var input = ":this-is-not-a-shortcode-because-it-is-way-too-long but here :fire: shows";
        var result = EmojiTable.Substitute(input);
        Assert.Contains("\U0001F525", result);
        Assert.StartsWith(":this-is-not-a-shortcode", result);
    }
}
