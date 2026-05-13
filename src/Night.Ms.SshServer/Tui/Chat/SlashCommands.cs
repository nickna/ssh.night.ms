namespace Night.Ms.SshServer.Tui.Chat;

// One source of truth for the slash-command grammar. Both CommandHighlighter (preview row
// coloring) and ChatScreen.HandleCommandAsync (dispatch) used to carry their own copy of the
// verb list — adding a new command meant updating both, with no compiler help if you forgot.
internal static class SlashCommands
{
    public enum ArgKind
    {
        Channel, // /join #lobby
        Handle,  // /dm alice, /finger alice
        Integer, // /react <n>, /pin <n>, /edit <n>, ...
        Emoji,   // /react n :+1: or /react n 👍
        Body,    // tail-of-line free text, inline-formatted
        Term,    // tail-of-line search term, no inline format
    }

    public static readonly IReadOnlyDictionary<string, ArgKind[]> Verbs =
        new Dictionary<string, ArgKind[]>(StringComparer.OrdinalIgnoreCase)
        {
            ["/help"]    = Array.Empty<ArgKind>(),
            ["/?"]       = Array.Empty<ArgKind>(),
            ["/quit"]    = Array.Empty<ArgKind>(),
            ["/exit"]    = Array.Empty<ArgKind>(),
            ["/who"]     = Array.Empty<ArgKind>(),
            ["/pins"]    = Array.Empty<ArgKind>(),
            ["/join"]    = new[] { ArgKind.Channel },
            ["/dm"]      = new[] { ArgKind.Handle },
            ["/finger"]  = new[] { ArgKind.Handle },
            ["/me"]      = new[] { ArgKind.Body },
            ["/topic"]   = new[] { ArgKind.Body },
            ["/search"]  = new[] { ArgKind.Term },
            ["/react"]   = new[] { ArgKind.Integer, ArgKind.Emoji },
            ["/unreact"] = new[] { ArgKind.Integer, ArgKind.Emoji },
            ["/pin"]     = new[] { ArgKind.Integer },
            ["/unpin"]   = new[] { ArgKind.Integer },
            ["/del"]     = new[] { ArgKind.Integer },
            ["/delete"]  = new[] { ArgKind.Integer },
            ["/edit"]    = new[] { ArgKind.Integer, ArgKind.Body },
            ["/reply"]   = new[] { ArgKind.Integer, ArgKind.Body },
            ["/thread"]  = new[] { ArgKind.Integer },
        };

    public const string HelpText =
        "Commands:\n" +
        "  /join #channel       switch to (or auto-create) a public channel\n" +
        "  /dm <handle>         open a direct message with another user\n" +
        "  /me <action>         emote in third-person (italic)\n" +
        "  /react <n> :emoji:   add a reaction to message n (1 = most recent)\n" +
        "  /unreact <n> :emoji: remove your reaction from message n\n" +
        "  /reply <n> <body>    post a threaded reply to message n\n" +
        "  /thread <n>          open a focused view of message n + its replies\n" +
        "  /edit <n> <body>     edit your message at position n\n" +
        "  /del <n>             delete your message at position n\n" +
        "  /pin <n>             pin message n (★ marker, listed by /pins)\n" +
        "  /unpin <n>           remove the pin marker\n" +
        "  /pins                list all pinned messages in this channel\n" +
        "  /topic <text>        set the channel topic (channel creator only)\n" +
        "  /search <term>       search recent messages in this channel\n" +
        "  /who                 show who's in this channel\n" +
        "  /finger <handle>     print a user's profile inline\n" +
        "  /quit                leave chat (back to lobby)\n" +
        "  /help                show this help\n" +
        "Formatting: *bold*  _italic_  `code`  @mention  :emoji:\n" +
        "Scrollback: PgUp / PgDn   |   jump to ends: Ctrl+Home / Ctrl+End\n" +
        "Switch channel: Alt+1..Alt+9 (slot number in left sidebar; Alt+0 = 10)";
}
