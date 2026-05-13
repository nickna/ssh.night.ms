namespace Night.Ms.SshServer.Tui.Chat;

// Curated emoji shortcode → unicode map. Deliberately small — we want every glyph to
// (a) render as a single grapheme on the major terminal fonts (Cascadia Mono, JetBrains
// Mono, Iosevka, Menlo, Source Code Pro) and (b) be useful enough to remember. The list
// covers the Slack/Discord "first reach" set: reactions, simple expressions, common nouns.
//
// The whole thing is a single static dictionary because (a) there are <100 entries so a
// hash lookup is fine, (b) it can be embedded into tests without loading Terminal.Gui, and
// (c) sysops can add entries by editing this file and rebuilding — no DB or config story.
internal static class EmojiTable
{
    public static readonly IReadOnlyDictionary<string, string> Map = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
    {
        // Faces / expressions
        ["smile"]       = "\U0001F600",
        ["grin"]        = "\U0001F601",
        ["joy"]         = "\U0001F602",
        ["laughing"]    = "\U0001F606",
        ["wink"]        = "\U0001F609",
        ["blush"]       = "\U0001F60A",
        ["sunglasses"]  = "\U0001F60E",
        ["thinking"]    = "\U0001F914",
        ["neutral"]     = "\U0001F610",
        ["expressionless"] = "\U0001F611",
        ["unamused"]    = "\U0001F612",
        ["sweat"]       = "\U0001F613",
        ["pensive"]     = "\U0001F614",
        ["confused"]    = "\U0001F615",
        ["worried"]     = "\U0001F61F",
        ["cry"]         = "\U0001F622",
        ["sob"]         = "\U0001F62D",
        ["scream"]      = "\U0001F631",
        ["angry"]       = "\U0001F620",
        ["rage"]        = "\U0001F621",
        ["heart_eyes"]  = "\U0001F60D",
        ["kiss"]        = "\U0001F618",
        ["zipper_mouth"] = "\U0001F910",
        ["sleeping"]    = "\U0001F634",
        ["dizzy_face"]  = "\U0001F635",
        ["nerd"]        = "\U0001F913",
        ["robot"]       = "\U0001F916",
        ["clown"]       = "\U0001F921",
        ["facepalm"]    = "\U0001F926",
        ["shrug"]       = "\U0001F937",
        ["wave"]        = "\U0001F44B",

        // Hands / gestures
        ["thumbsup"]    = "\U0001F44D",
        ["+1"]          = "\U0001F44D",
        ["thumbsdown"]  = "\U0001F44E",
        ["-1"]          = "\U0001F44E",
        ["clap"]        = "\U0001F44F",
        ["raised_hands"] = "\U0001F64C",
        ["pray"]        = "\U0001F64F",
        ["ok_hand"]     = "\U0001F44C",
        ["point_up"]    = "☝️",
        ["muscle"]      = "\U0001F4AA",
        ["fist"]        = "✊",
        ["v"]           = "✌️",

        // Symbols / hearts
        ["heart"]       = "❤️",
        ["broken_heart"] = "\U0001F494",
        ["sparkles"]    = "✨",
        ["star"]        = "⭐",
        ["fire"]        = "\U0001F525",
        ["100"]         = "\U0001F4AF",
        ["check"]       = "✅",
        ["x"]           = "❌",
        ["warning"]     = "⚠️",
        ["question"]    = "❓",
        ["exclamation"] = "❗",
        ["zap"]         = "⚡",
        ["boom"]        = "\U0001F4A5",
        ["sparkle"]     = "❇️",
        ["eyes"]        = "\U0001F440",
        ["bulb"]        = "\U0001F4A1",
        ["tada"]        = "\U0001F389",
        ["rocket"]      = "\U0001F680",
        ["bug"]         = "\U0001F41B",
        ["beer"]        = "\U0001F37A",
        ["coffee"]      = "☕",
        ["pizza"]       = "\U0001F355",
        ["cake"]        = "\U0001F370",
        ["cat"]         = "\U0001F408",
        ["dog"]         = "\U0001F415",
        ["robot_face"]  = "\U0001F916",
        ["computer"]    = "\U0001F4BB",
        ["phone"]       = "\U0001F4F1",
        ["mailbox"]     = "\U0001F4EE",
        ["lock"]        = "\U0001F512",
        ["key"]         = "\U0001F511",
        ["moon"]        = "\U0001F319",
        ["sun"]         = "☀️",
        ["cloud"]       = "☁️",
        ["snowflake"]   = "❄️",
        ["umbrella"]    = "☔",
        ["tree"]        = "\U0001F333",
        ["earth"]       = "\U0001F30D",

        // Plumbing
        ["shipit"]      = "\U0001F69A",
        ["ship"]        = "\U0001F6A2",
        ["hammer"]      = "\U0001F528",
        ["wrench"]      = "\U0001F527",
        ["package"]     = "\U0001F4E6",
        ["construction"] = "\U0001F6A7",
        ["lgtm"]        = "\U0001F44D",
    };

    // Returns the original text with :shortcode: tokens replaced. Unknown shortcodes are
    // left untouched (no error, just pass through) so a typo doesn't eat the rest of the
    // message. A leading or trailing colon without a closing pair is treated as literal.
    public static string Substitute(string text)
    {
        if (string.IsNullOrEmpty(text) || text.IndexOf(':') < 0) return text;

        var output = new System.Text.StringBuilder(text.Length);
        var i = 0;
        while (i < text.Length)
        {
            if (text[i] != ':')
            {
                output.Append(text[i]);
                i++;
                continue;
            }
            // Find the matching closing ':' within a reasonable window. Shortcodes are
            // short (the longest in our table is 13 chars) so cap the lookahead to keep
            // pathological inputs cheap.
            var close = -1;
            for (var j = i + 1; j < Math.Min(i + 32, text.Length); j++)
            {
                var c = text[j];
                if (c == ':') { close = j; break; }
                if (!IsShortcodeChar(c)) break;
            }
            if (close > i + 1)
            {
                var code = text.Substring(i + 1, close - i - 1);
                if (Map.TryGetValue(code, out var glyph))
                {
                    output.Append(glyph);
                    i = close + 1;
                    continue;
                }
            }
            output.Append(':');
            i++;
        }
        return output.ToString();
    }

    private static bool IsShortcodeChar(char c) =>
        char.IsAsciiLetterOrDigit(c) || c == '_' || c == '+' || c == '-';
}
