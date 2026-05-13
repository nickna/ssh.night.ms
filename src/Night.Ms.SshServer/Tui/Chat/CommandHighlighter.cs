using Night.Ms.SshServer.Tui.Art;
using ArgKind = Night.Ms.SshServer.Tui.Chat.SlashCommands.ArgKind;

namespace Night.Ms.SshServer.Tui.Chat;

// Tokenizes a slash-command buffer into a colored ChatLine for the input-preview row.
// Verbs come out cyan-bold (known) or red (unknown); each known verb has a fixed argument
// grammar that drives per-token coloring — integer args validate against int.TryParse, emoji
// args want :shortcode: or a non-ASCII rune, channel args expect a leading '#', handle args
// match the same charset as Handle validation. Body args (free text after `/me`, `/topic`,
// `/edit n`, `/reply n`) are run through MessageRenderer.PreviewBody so inline format
// markers preview as they will in the sent message.
//
// Pure data — no Terminal.Gui dependency — so the tests can assert on the run list directly.
internal static class CommandHighlighter
{
    // Cyan accents match MessageRenderer's mention color so the eye groups command-mode
    // and chat-mode highlighting into one palette.
    private static readonly ArtColor VerbKnownFg   = new(0x6C, 0xC0, 0xFF);
    private static readonly ArtColor VerbUnknownFg = new(0xFF, 0x70, 0x70);
    private static readonly ArtColor IntOkFg       = new(0xFF, 0xC8, 0x4C); // yellow
    private static readonly ArtColor ChannelFg     = new(0x6C, 0xC0, 0xFF);
    private static readonly ArtColor HandleFg      = new(0x6C, 0xC0, 0xFF);
    private static readonly ArtColor EmojiFg       = new(0x9F, 0xE5, 0x9F);
    private static readonly ArtColor TermFg        = new(0xFF, 0xFF, 0xFF);
    private static readonly ArtColor ArgErrorFg    = new(0xFF, 0x70, 0x70);

    // Pre-condition: text must be non-empty and start with '/' (callers branch on the prefix
    // and send chat-body input to MessageRenderer.PreviewBody instead).
    public static ChatLine Highlight(string text, string selfHandle)
    {
        var runs = new List<ChatRun>(8);

        // Carve off the verb (up to first space or EOL).
        var spaceIdx = text.IndexOf(' ');
        var verbText = spaceIdx < 0 ? text : text[..spaceIdx];
        var known = SlashCommands.Verbs.TryGetValue(verbText, out var argKinds);
        runs.Add(new ChatRun(verbText, known ? VerbKnownFg : VerbUnknownFg, ArtStyle.Bold));

        var i = spaceIdx < 0 ? text.Length : spaceIdx;

        if (!known || argKinds is null)
        {
            // Unknown verb: paint the rest plain so the whole row doesn't read as an error.
            if (i < text.Length)
                runs.Add(ChatRun.Plain(text[i..]));
            return new ChatLine(runs);
        }

        for (var argIdx = 0; argIdx < argKinds.Length && i < text.Length; argIdx++)
        {
            // Whitespace between verb/args paints plain so the spacing reads naturally.
            var wsStart = i;
            while (i < text.Length && text[i] == ' ') i++;
            if (i > wsStart) runs.Add(ChatRun.Plain(text[wsStart..i]));
            if (i >= text.Length) break;

            var kind = argKinds[argIdx];
            if (kind == ArgKind.Body)
            {
                // Tail body — defer to chat-message preview so *bold*/_italic_/`code`/
                // @mention/:emoji: tokens look the same as they will when sent.
                var body = text[i..];
                foreach (var r in MessageRenderer.PreviewBody(body, selfHandle).Runs)
                    runs.Add(r);
                i = text.Length;
                break;
            }
            if (kind == ArgKind.Term)
            {
                runs.Add(new ChatRun(text[i..], TermFg, ArtStyle.None));
                i = text.Length;
                break;
            }

            // Single space-delimited token.
            var tokStart = i;
            while (i < text.Length && text[i] != ' ') i++;
            runs.Add(ColorToken(text[tokStart..i], kind));
        }

        // Anything left over (verb supplied extra args) — paint plain rather than error.
        if (i < text.Length)
            runs.Add(ChatRun.Plain(text[i..]));

        return new ChatLine(runs);
    }

    private static ChatRun ColorToken(string token, ArgKind kind)
    {
        switch (kind)
        {
            case ArgKind.Integer:
                var ok = int.TryParse(token, out var n) && n > 0;
                return new ChatRun(token, ok ? IntOkFg : ArgErrorFg, ArtStyle.Bold);
            case ArgKind.Channel:
                return token.StartsWith("#", StringComparison.Ordinal)
                    ? new ChatRun(token, ChannelFg, ArtStyle.Bold)
                    : new ChatRun(token, ArgErrorFg, ArtStyle.None);
            case ArgKind.Handle:
                return HandleLooksValid(token)
                    ? new ChatRun(token, HandleFg, ArtStyle.Bold)
                    : new ChatRun(token, ArgErrorFg, ArtStyle.None);
            case ArgKind.Emoji:
                return LooksLikeEmoji(token)
                    ? new ChatRun(token, EmojiFg, ArtStyle.Bold)
                    : new ChatRun(token, ArgErrorFg, ArtStyle.None);
            default:
                return ChatRun.Plain(token);
        }
    }

    private static bool HandleLooksValid(string token)
    {
        if (token.Length is 0 or > 32) return false;
        foreach (var c in token)
            if (!char.IsLetterOrDigit(c) && c != '_' && c != '-') return false;
        return true;
    }

    private static bool LooksLikeEmoji(string token)
    {
        // :shortcode: (at least one char between colons).
        if (token.Length >= 3 && token[0] == ':' && token[^1] == ':') return true;
        // Single non-ASCII rune (👍, ❤, ✨ …). Reject longer mixed strings so typos don't
        // accidentally read as valid.
        var runes = token.EnumerateRunes();
        if (!runes.MoveNext()) return false;
        var first = runes.Current;
        if (runes.MoveNext()) return false; // more than one rune
        return first.Value > 0x7F;
    }
}
