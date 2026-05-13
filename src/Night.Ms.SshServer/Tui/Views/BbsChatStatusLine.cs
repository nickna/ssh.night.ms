using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Views;

// Status row for ChatScreen that paints per-run color/style instead of a single Scheme. Used
// so the channel topic can render *bold*/_italic_/`code`/:emoji: the same way message bodies
// do. Mirrors the public surface of BbsStatusLine (Set/SetWarning/SetSuccess) for the plain
// cases, plus a SetStyled(ChatLine) entry point for the topic refresh path.
//
// Single-row: a stray newline ends the paint at the current column rather than wrapping.
internal sealed class BbsChatStatusLine : View
{
    private ChatLine? _line;

    public BbsChatStatusLine()
    {
        CanFocus = false;
    }

    public void SetStyled(ChatLine line)
    {
        _line = line;
        SetNeedsDraw();
    }

    // Auto-routes "[!] ..." to warning red, anything else to the dim Status look. Matches
    // BbsStatusLine's heuristic so existing call sites in ChatScreen don't need to track
    // which kind they want.
    public void Set(string text)
    {
        if (text.StartsWith("[!]", StringComparison.Ordinal))
        {
            SetWarning(text);
        }
        else
        {
            SetStyled(BuildPlain(text, ChatPalette.SystemInfo, ArtStyle.None));
        }
    }

    public void SetWarning(string text) => SetStyled(BuildPlain(text, ChatPalette.SystemError, ArtStyle.Bold));

    private static ChatLine BuildPlain(string text, ArtColor color, ArtStyle style)
        => new(new[] { new ChatRun(text, color, style) });

    protected override bool OnDrawingContent(DrawContext? context)
    {
        var width = Viewport.Width;
        var height = Viewport.Height;
        if (width <= 0 || height <= 0) return true;

        SetAttribute(Default);
        for (var x = 0; x < width; x++) AddRune(x, 0, (Rune)' ');

        if (_line is null) return true;

        var col = 0;
        foreach (var run in _line.Runs)
        {
            SetAttribute(ToAttribute(run));
            foreach (var rune in run.Text.EnumerateRunes())
            {
                if (col >= width) return true;
                if (rune.Value == '\n' || rune.Value == '\r') return true;
                AddRune(col, 0, rune);
                col += Math.Max(1, RuneWidths.Of(rune));
            }
        }
        return true;
    }

    private static Attribute ToAttribute(ChatRun run)
    {
        var fg = new Color(run.Foreground.R, run.Foreground.G, run.Foreground.B);
        var bg = new Color(0, 0, 0);
        var ts = TextStyle.None;
        if (run.Style.HasFlag(ArtStyle.Bold))      ts |= TextStyle.Bold;
        if (run.Style.HasFlag(ArtStyle.Italic))    ts |= TextStyle.Italic;
        if (run.Style.HasFlag(ArtStyle.Underline)) ts |= TextStyle.Underline;
        return new Attribute(fg, bg, ts);
    }

    private static readonly Attribute Default = new(
        new Color(ArtColor.DefaultForeground.R, ArtColor.DefaultForeground.G, ArtColor.DefaultForeground.B),
        new Color(0, 0, 0));
}
