using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Chat;

// Named RGB colors used throughout chat rendering. Owns colors that don't map cleanly to
// BbsTheme (which is TG-side, 16-color) — chat uses true-color RGB through ArtColor so it
// can pick handle-coloring hashes that the 16-color palette can't express.
internal static class ChatPalette
{
    // Dim gray "[12:34]" timestamp + ": " separator + reply-arrow chrome.
    public static readonly ArtColor Chrome = new(0x70, 0x70, 0x70);

    // Other-user mention (cyan) and self-mention (bright yellow bold + flag).
    public static readonly ArtColor MentionOther = new(0x6C, 0xC0, 0xFF);
    public static readonly ArtColor MentionSelf  = new(0xFF, 0xD7, 0x00);

    // Inline format colors — body text inherits the default fg; the markers themselves are
    // not emitted, only the styled content between them.
    public static readonly ArtColor BoldFg = new(0xFF, 0xFF, 0xFF);
    public static readonly ArtColor CodeFg = new(0x9F, 0xE5, 0x9F);

    // Yellow ★ marker prepended to pinned messages.
    public static readonly ArtColor Pin = new(0xFF, 0xC8, 0x4C);

    // Tombstone color for deleted message bodies (faint gray, italic at the call site).
    public static readonly ArtColor Deleted = new(0x80, 0x80, 0x80);

    // System lines — info ("--- joined #lobby ---") and error ("[!] permission denied").
    public static readonly ArtColor SystemInfo  = new(0x70, 0xC0, 0xC0);
    public static readonly ArtColor SystemError = new(0xFF, 0x70, 0x70);

    // Reaction footer chips. ReactionByMe paints bold yellow so the reaction the current
    // user contributed reads as toggleable; ReactionByOther stays dim gray.
    public static readonly ArtColor ReactionByMe    = MentionSelf;
    public static readonly ArtColor ReactionByOther = new(0xB0, 0xB0, 0xB0);
}
