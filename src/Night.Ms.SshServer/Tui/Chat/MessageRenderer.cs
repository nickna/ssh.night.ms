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

    // All per-token colors live in ChatPalette so the renderer can be re-themed in one file.

    // Standard message: "[clock] handle: body". Optional decorations:
    //   pinned    → "★ " marker before the chrome.
    //   replyTo   → "↳ @parent " prefix between chrome and body. Identifies thread context
    //               without a separate view.
    //   replyCount → " [N replies]" suffix after the body — only rendered on top-level
    //               messages that have children.
    //   edited    → " (edited)" suffix in italic.
    //   hasPfp    → render a leading "●" dot in the sender's color, before the handle, when
    //               the author has a profile picture uploaded. Pure visual signal — gives
    //               readers an "this person has a face" cue without bloating the line.
    public static ChatLine RenderMessage(
        string clock,
        string senderHandle,
        string body,
        string selfHandle,
        bool edited = false,
        bool pinned = false,
        string? replyToHandle = null,
        int replyCount = 0,
        bool hasPfp = false)
    {
        var runs = new List<ChatRun>(8);
        if (pinned)
        {
            runs.Add(new ChatRun("★ ", ChatPalette.Pin, ArtStyle.Bold));
        }
        runs.Add(new ChatRun($"[{clock}] ", ChatPalette.Chrome, ArtStyle.None));
        var senderColor = HandleColorizer.ColorFor(senderHandle);
        if (hasPfp)
        {
            runs.Add(new ChatRun("● ", senderColor, ArtStyle.None));
        }
        runs.Add(new ChatRun(senderHandle, senderColor, ArtStyle.Bold));
        runs.Add(new ChatRun(": ", ChatPalette.Chrome, ArtStyle.None));
        if (!string.IsNullOrEmpty(replyToHandle))
        {
            runs.Add(new ChatRun("↳ @", ChatPalette.Chrome, ArtStyle.None));
            runs.Add(new ChatRun(replyToHandle, ChatPalette.MentionOther, ArtStyle.None));
            runs.Add(new ChatRun(" ", ChatPalette.Chrome, ArtStyle.None));
        }

        var selfMentioned = AppendBodyRuns(runs, body, selfHandle);
        if (edited)
        {
            runs.Add(new ChatRun(" (edited)", ChatPalette.Chrome, ArtStyle.Italic));
        }
        if (replyCount > 0)
        {
            var label = replyCount == 1 ? "1 reply" : $"{replyCount} replies";
            runs.Add(new ChatRun($"  [{label}]", ChatPalette.Chrome, ArtStyle.Italic));
        }
        return new ChatLine(runs, selfMentioned);
    }

    // Tombstone for deleted messages — chrome stays so reactions/scrollback context remain
    // anchored, but the body itself reads "(deleted)" in faint gray.
    public static ChatLine RenderDeleted(string clock, string senderHandle)
    {
        var runs = new List<ChatRun>(4);
        runs.Add(new ChatRun($"[{clock}] ", ChatPalette.Chrome, ArtStyle.None));
        runs.Add(new ChatRun(senderHandle, HandleColorizer.ColorFor(senderHandle), ArtStyle.None));
        runs.Add(new ChatRun(": ", ChatPalette.Chrome, ArtStyle.None));
        runs.Add(new ChatRun("(deleted)", ChatPalette.Deleted, ArtStyle.Italic));
        return new ChatLine(runs);
    }

    // /me variant: "[clock] * alice waves at the lobby" — italicized whole-line emote.
    public static ChatLine RenderEmote(string clock, string senderHandle, string action, string selfHandle, bool hasPfp = false)
    {
        var runs = new List<ChatRun>(4);
        runs.Add(new ChatRun($"[{clock}] ", ChatPalette.Chrome, ArtStyle.None));
        var nameColor = HandleColorizer.ColorFor(senderHandle);
        if (hasPfp)
        {
            runs.Add(new ChatRun("● ", nameColor, ArtStyle.None));
        }
        runs.Add(new ChatRun($"* {senderHandle} ", nameColor, ArtStyle.Italic | ArtStyle.Bold));
        var selfMentioned = AppendBodyRuns(runs, action, selfHandle, baseStyle: ArtStyle.Italic);
        return new ChatLine(runs, selfMentioned);
    }

    // System lines: "---- joined #lobby ----", "[!] permission denied", etc. No body parsing.
    public static ChatLine RenderSystem(string text, bool isError = false)
    {
        var color = isError ? ChatPalette.SystemError : ChatPalette.SystemInfo;
        return new ChatLine(new[] { new ChatRun(text, color, isError ? ArtStyle.Bold : ArtStyle.None) });
    }

    // Plain unstyled line — used for /finger output and other multi-line dumps we don't
    // want to re-parse for format markers (a fingerprint with underscores would otherwise
    // get italicized).
    public static ChatLine RenderRaw(string text) =>
        new(new[] { ChatRun.Plain(text) });

    // Body-only render with no chrome — used by the input-preview row so the user sees how
    // *bold*/_italic_/`code`/@mention/:emoji: will paint before they press Enter. The
    // returned ChatLine carries the same SelfMentioned flag as RenderMessage so the caller
    // can wire mention feedback if it wants.
    public static ChatLine PreviewBody(string text, string selfHandle)
    {
        var runs = new List<ChatRun>(4);
        var selfMentioned = AppendBodyRuns(runs, text ?? string.Empty, selfHandle);
        return new ChatLine(runs, selfMentioned);
    }

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
                    isSelf ? ChatPalette.MentionSelf : ChatPalette.MentionOther,
                    baseStyle | ArtStyle.Bold));
            }
            else if (m.Groups["bold"].Success)
            {
                runs.Add(new ChatRun(m.Value[1..^1], ChatPalette.BoldFg, baseStyle | ArtStyle.Bold));
            }
            else if (m.Groups["italic"].Success)
            {
                runs.Add(new ChatRun(m.Value[1..^1], ArtColor.DefaultForeground, baseStyle | ArtStyle.Italic));
            }
            else if (m.Groups["code"].Success)
            {
                runs.Add(new ChatRun(m.Value[1..^1], ChatPalette.CodeFg, baseStyle));
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
