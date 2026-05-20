using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Views;

// One-row companion view above the chat compose field that paints a colorized rendering of
// whatever the user is typing — slash command tokens via CommandHighlighter, chat-body via
// MessageRenderer.PreviewBody. The host screen toggles Visible+Height between 0/1 so the
// row collapses entirely when the buffer is empty.
//
// Single-line clamp: a stray newline in the buffer ends the paint at column 0 rather than
// wrapping, since the input field itself is single-line.
internal sealed class ChatInputPreview : View
{
    private ChatLine? _line;

    public ChatInputPreview()
    {
        CanFocus = false;
    }

    public void SetLine(ChatLine? line)
    {
        _line = line;
        SetNeedsDraw();
    }

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
            SetAttribute(AttributeCache.For(run.Foreground, run.Style));
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

    private static readonly Attribute Default = AttributeCache.For(ArtColor.DefaultForeground, ArtStyle.None);
}
