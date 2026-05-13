using System.Text.RegularExpressions;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Chat;

// Turns one chat message into a styled ChatLine. The pipeline:
//
//   1. Substitute :emoji: shortcodes in the body. Done first so a shortcode inside a *bold*
//      span still gets the glyph, and so the inline-format pass doesn't trip on colons.
//   2. Emit the chrome: dim "[HH:mm] ", colored-per-handle sender, dim ": ".
//   3. Tokenize the body for inline format (`*bold*`, `_italic_`, `` `code` ``) and
//      @mentions, emitting one ChatRun per token and one for each plain segment between.
//
// All rendering output is pure data — no Terminal.Gui dependency. ChatLogView maps the
// runs to TG attributes at paint time. Tests can assert on the run list directly.
internal static class MessageRenderer
{
    // Format tokens (greedy, non-nesting) + mentions. We match the first alternative that
    // wins at any given position. Bold/italic/code require a non-space immediately inside
    // both markers so we don't eat a leading "*" used for emphasis on a phrase end, and
    // don't match across newlines. Mention requires a word boundary on the left so an
    // email-like "x@host" isn't mistaken for an @-mention.
    private static readonly Regex Inline = new(
        @"(?<mention>(?<![A-Za-z0-9_])@(?<who>[A-Za-z0-9][A-Za-z0-9_-]{0,31}))" +
        @"|(?<bold>\*(?=[^\s*])[^*\n]*?(?<=[^\s*])\*)" +
        @"|(?<italic>_(?=[^\s_])[^_\n]*?(?<=[^\s_])_)" +
        @"|(?<code>`[^`\n]+?`)",
        RegexOptions.Compiled);

    // Dim gray "[12:34]" timestamp + ": " separator.
    private static readonly ArtColor Chrome = new(0x70, 0x70, 0x70);

    // Other-user mention (cyan) and self-mention (bright yellow bold + flag).
    private static readonly ArtColor MentionOther = new(0x6C, 0xC0, 0xFF);
    private static readonly ArtColor MentionSelf  = new(0xFF, 0xD7, 0x00);

    // Inline format colors — body text inherits the default fg; the markers themselves are
    // not emitted, only the styled content between them.
    private static readonly ArtColor BoldFg   = new(0xFF, 0xFF, 0xFF);
    private static readonly ArtColor CodeFg   = new(0x9F, 0xE5, 0x9F);

    // Yellow ★ marker prepended to pinned messages. One cell wide so it doesn't disturb
    // the column alignment for users who aren't using a font with full emoji width.
    private static readonly ArtColor PinColor = new(0xFF, 0xC8, 0x4C);

    // Standard message: "[clock] handle: body". Optional decorations:
    //   pinned    → "★ " marker before the chrome.
    //   replyTo   → "↳ @parent " prefix between chrome and body. Identifies thread context
    //               without a separate view.
    //   replyCount → " [N replies]" suffix after the body — only rendered on top-level
    //               messages that have children.
    //   edited    → " (edited)" suffix in italic.
    public static ChatLine RenderMessage(
        string clock,
        string senderHandle,
        string body,
        string selfHandle,
        bool edited = false,
        bool pinned = false,
        string? replyToHandle = null,
        int replyCount = 0)
    {
        var runs = new List<ChatRun>(8);
        if (pinned)
        {
            runs.Add(new ChatRun("★ ", PinColor, ArtStyle.Bold));
        }
        runs.Add(new ChatRun($"[{clock}] ", Chrome, ArtStyle.None));
        runs.Add(new ChatRun(senderHandle, HandleColorizer.ColorFor(senderHandle), ArtStyle.Bold));
        runs.Add(new ChatRun(": ", Chrome, ArtStyle.None));
        if (!string.IsNullOrEmpty(replyToHandle))
        {
            runs.Add(new ChatRun("↳ @", Chrome, ArtStyle.None));
            runs.Add(new ChatRun(replyToHandle, MentionOther, ArtStyle.None));
            runs.Add(new ChatRun(" ", Chrome, ArtStyle.None));
        }

        var selfMentioned = AppendBodyRuns(runs, body, selfHandle);
        if (edited)
        {
            runs.Add(new ChatRun(" (edited)", Chrome, ArtStyle.Italic));
        }
        if (replyCount > 0)
        {
            var label = replyCount == 1 ? "1 reply" : $"{replyCount} replies";
            runs.Add(new ChatRun($"  [{label}]", Chrome, ArtStyle.Italic));
        }
        return new ChatLine(runs, selfMentioned);
    }

    // Tombstone for deleted messages — chrome stays so reactions/scrollback context remain
    // anchored, but the body itself reads "(deleted)" in faint gray.
    public static ChatLine RenderDeleted(string clock, string senderHandle)
    {
        var runs = new List<ChatRun>(4);
        runs.Add(new ChatRun($"[{clock}] ", Chrome, ArtStyle.None));
        runs.Add(new ChatRun(senderHandle, HandleColorizer.ColorFor(senderHandle), ArtStyle.None));
        runs.Add(new ChatRun(": ", Chrome, ArtStyle.None));
        runs.Add(new ChatRun("(deleted)", new ArtColor(0x80, 0x80, 0x80), ArtStyle.Italic));
        return new ChatLine(runs);
    }

    // /me variant: "[clock] * alice waves at the lobby" — italicized whole-line emote.
    public static ChatLine RenderEmote(string clock, string senderHandle, string action, string selfHandle)
    {
        var runs = new List<ChatRun>(4);
        runs.Add(new ChatRun($"[{clock}] ", Chrome, ArtStyle.None));
        var nameColor = HandleColorizer.ColorFor(senderHandle);
        runs.Add(new ChatRun($"* {senderHandle} ", nameColor, ArtStyle.Italic | ArtStyle.Bold));
        var selfMentioned = AppendBodyRuns(runs, action, selfHandle, baseStyle: ArtStyle.Italic);
        return new ChatLine(runs, selfMentioned);
    }

    // System lines: "---- joined #lobby ----", "[!] permission denied", etc. No body parsing.
    public static ChatLine RenderSystem(string text, bool isError = false)
    {
        var color = isError ? new ArtColor(0xFF, 0x70, 0x70) : new ArtColor(0x70, 0xC0, 0xC0);
        return new ChatLine(new[] { new ChatRun(text, color, isError ? ArtStyle.Bold : ArtStyle.None) });
    }

    // Plain unstyled line — used for /finger output and other multi-line dumps we don't
    // want to re-parse for format markers (a fingerprint with underscores would otherwise
    // get italicized).
    public static ChatLine RenderRaw(string text) =>
        new(new[] { ChatRun.Plain(text) });

    private static bool AppendBodyRuns(List<ChatRun> runs, string body, string selfHandle, ArtStyle baseStyle = ArtStyle.None)
    {
        var text = EmojiTable.Substitute(body ?? string.Empty);
        if (text.Length == 0) return false;

        var selfMentioned = false;
        var cursor = 0;
        foreach (Match m in Inline.Matches(text))
        {
            if (m.Index > cursor)
            {
                runs.Add(new ChatRun(text.Substring(cursor, m.Index - cursor), ArtColor.DefaultForeground, baseStyle));
            }

            if (m.Groups["mention"].Success)
            {
                var who = m.Groups["who"].Value;
                var isSelf = !string.IsNullOrEmpty(selfHandle)
                          && string.Equals(who, selfHandle, StringComparison.OrdinalIgnoreCase);
                if (isSelf) selfMentioned = true;
                runs.Add(new ChatRun(
                    m.Value,
                    isSelf ? MentionSelf : MentionOther,
                    baseStyle | ArtStyle.Bold));
            }
            else if (m.Groups["bold"].Success)
            {
                runs.Add(new ChatRun(m.Value[1..^1], BoldFg, baseStyle | ArtStyle.Bold));
            }
            else if (m.Groups["italic"].Success)
            {
                runs.Add(new ChatRun(m.Value[1..^1], ArtColor.DefaultForeground, baseStyle | ArtStyle.Italic));
            }
            else if (m.Groups["code"].Success)
            {
                runs.Add(new ChatRun(m.Value[1..^1], CodeFg, baseStyle));
            }

            cursor = m.Index + m.Length;
        }

        if (cursor < text.Length)
        {
            runs.Add(new ChatRun(text[cursor..], ArtColor.DefaultForeground, baseStyle));
        }
        return selfMentioned;
    }
}
