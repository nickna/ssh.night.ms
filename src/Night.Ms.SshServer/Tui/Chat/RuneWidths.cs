using System.Text;

namespace Night.Ms.SshServer.Tui.Chat;

// Display-width heuristic for runes used by the chat preview + log views. The two views
// MUST stay in lock-step so the preview's column accounting tracks the rendered message —
// when they diverged hand-rolled, an emoji typed at the input wrapped at a different column
// than it rendered at. Shared here so the heuristic lives in one place.
internal static class RuneWidths
{
    // 1 for ASCII + Latin-Supplement, 2 for the emoji/CJK ranges xterm reports as wide.
    // Not a full grapheme analysis — fine for the BBS use case where messages are short
    // and the rare miss is recoverable by manual wrap.
    public static int Of(Rune r)
    {
        var v = r.Value;
        if (v < 0x300) return 1;
        if (v >= 0x1F300 && v <= 0x1FAFF) return 2;
        if (v >= 0x2600 && v <= 0x27BF)   return 2;
        if (v >= 0x3000 && v <= 0x9FFF)   return 2;
        if (v >= 0xFE30 && v <= 0xFE4F)   return 2;
        if (v >= 0xFF00 && v <= 0xFF60)   return 2;
        return 1;
    }
}
