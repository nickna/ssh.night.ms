using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Screens;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Views;

// Centered horizontal carousel of lobby destination cards. The selected entry is drawn larger
// with a double-line accent border at the visual center; unselected neighbours fan out in both
// directions with a single-line faint border, clipping cleanly at the viewport edges. ←/→ (or
// H/L) re-centers; Enter fires EntryActivated. Direct-jump hotkeys are owned by the parent screen
// and arrive via TrySelectByHotkey.
internal sealed class LobbyCarouselView : View
{
    public sealed record Entry(string Label, Key Hotkey, LobbyNavigation Target, CellGrid Icon);

    public const int RowHeight = 6;

    private const int SelectedWidth = 16;
    private const int UnselectedWidth = 14;
    private const int UnselectedHeight = 5;
    private const int Gap = 2;

    private readonly IReadOnlyList<Entry> _entries;
    private int _index;

    public LobbyCarouselView(IReadOnlyList<Entry> entries)
    {
        if (entries.Count == 0) throw new ArgumentException("at least one entry required", nameof(entries));
        _entries = entries;
        CanFocus = true;
        Height = RowHeight;

        KeyDown += OnKey;
    }

    public int SelectedIndex => _index;
    public LobbyNavigation SelectedTarget => _entries[_index].Target;
    public IReadOnlyList<Entry> Entries => _entries;

    public event EventHandler<LobbyNavigation>? EntryActivated;

    public void MoveLeft()
    {
        _index = (_index - 1 + _entries.Count) % _entries.Count;
        SetNeedsDraw();
    }

    public void MoveRight()
    {
        _index = (_index + 1) % _entries.Count;
        SetNeedsDraw();
    }

    public bool TrySelectByHotkey(Key key)
    {
        for (var i = 0; i < _entries.Count; i++)
        {
            var hk = _entries[i].Hotkey;
            if (key == hk || key == hk.WithShift)
            {
                if (_index != i)
                {
                    _index = i;
                    SetNeedsDraw();
                }
                return true;
            }
        }
        return false;
    }

    private void OnKey(object? sender, Key key)
    {
        if (key == Key.CursorLeft || key == Key.H || key == Key.H.WithShift)
        {
            MoveLeft();
            key.Handled = true;
            return;
        }
        if (key == Key.CursorRight || key == Key.L || key == Key.L.WithShift)
        {
            MoveRight();
            key.Handled = true;
            return;
        }
        if (key == Key.Enter)
        {
            key.Handled = true;
            EntryActivated?.Invoke(this, SelectedTarget);
            return;
        }
    }

    protected override bool OnDrawingContent(DrawContext? context)
    {
        var viewportWidth = Viewport.Width;
        if (viewportWidth <= 0) return base.OnDrawingContent(context);

        SetAttribute(new Attribute(BbsTheme.Body, BbsTheme.Bg));
        for (var y = 0; y < RowHeight; y++)
        {
            for (var x = 0; x < viewportWidth; x++)
            {
                AddRune(x, y, new Rune(' '));
            }
        }

        var cx = viewportWidth / 2;
        var selectedLeft = cx - SelectedWidth / 2;

        PaintCard(selectedLeft, 0, SelectedWidth, RowHeight, selected: true, _entries[_index], viewportWidth);

        var rightEdge = selectedLeft + SelectedWidth + Gap;
        for (var step = 1; step <= _entries.Count - 1; step++)
        {
            if (rightEdge >= viewportWidth) break;
            var entry = _entries[(_index + step) % _entries.Count];
            PaintCard(rightEdge, RowHeight - UnselectedHeight, UnselectedWidth, UnselectedHeight,
                selected: false, entry, viewportWidth);
            rightEdge += UnselectedWidth + Gap;
        }

        var leftEdge = selectedLeft - Gap - UnselectedWidth;
        for (var step = 1; step <= _entries.Count - 1; step++)
        {
            if (leftEdge + UnselectedWidth <= 0) break;
            var entry = _entries[(_index - step + _entries.Count) % _entries.Count];
            PaintCard(leftEdge, RowHeight - UnselectedHeight, UnselectedWidth, UnselectedHeight,
                selected: false, entry, viewportWidth);
            leftEdge -= UnselectedWidth + Gap;
        }

        return true;
    }

    private void PaintCard(int leftX, int topY, int width, int height, bool selected, Entry entry, int viewportWidth)
    {
        var borderColor = selected ? BbsTheme.Accent : BbsTheme.Faint;
        var labelColor = selected ? BbsTheme.Header : BbsTheme.Body;
        var bg = BbsTheme.Bg;

        var (tl, tr, bl, br, h, v) = selected
            ? ('╔', '╗', '╚', '╝', '═', '║')
            : ('┌', '┐', '└', '┘', '─', '│');

        SetAttribute(new Attribute(borderColor, bg, selected ? TextStyle.Bold : TextStyle.None));

        Plot(leftX, topY, new Rune(tl), viewportWidth);
        Plot(leftX + width - 1, topY, new Rune(tr), viewportWidth);
        Plot(leftX, topY + height - 1, new Rune(bl), viewportWidth);
        Plot(leftX + width - 1, topY + height - 1, new Rune(br), viewportWidth);
        for (var x = 1; x < width - 1; x++)
        {
            Plot(leftX + x, topY, new Rune(h), viewportWidth);
            Plot(leftX + x, topY + height - 1, new Rune(h), viewportWidth);
        }
        for (var y = 1; y < height - 1; y++)
        {
            Plot(leftX, topY + y, new Rune(v), viewportWidth);
            Plot(leftX + width - 1, topY + y, new Rune(v), viewportWidth);
        }

        var innerWidth = width - 2;
        var innerLeft = leftX + 1;

        var icon = entry.Icon;
        var iconRows = Math.Min(2, icon.Height);
        var iconStartY = selected ? topY + 2 : topY + 1;
        var iconCols = Math.Min(icon.Width, innerWidth);
        var iconStartX = innerLeft + (innerWidth - iconCols) / 2;
        for (var iy = 0; iy < iconRows; iy++)
        {
            for (var ix = 0; ix < iconCols; ix++)
            {
                var cell = icon[ix, iy];
                var fg = new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B, 0xFF);
                var cbg = new Color(cell.Background.R, cell.Background.G, cell.Background.B, 0xFF);
                var style = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
                SetAttribute(new Attribute(fg, cbg, style));
                Plot(iconStartX + ix, iconStartY + iy, cell.Glyph, viewportWidth);
            }
        }

        var labelText = selected ? $"► {entry.Label.ToUpperInvariant()} ◄" : entry.Label;
        if (labelText.Length > innerWidth) labelText = labelText[..innerWidth];
        var labelLen = labelText.Length;
        var labelStartX = innerLeft + (innerWidth - labelLen) / 2;
        var labelY = topY + height - 2;
        SetAttribute(new Attribute(labelColor, bg, selected ? TextStyle.Bold : TextStyle.None));
        for (var i = 0; i < labelLen; i++)
        {
            Plot(labelStartX + i, labelY, new Rune(labelText[i]), viewportWidth);
        }
    }

    private void Plot(int x, int y, Rune r, int viewportWidth)
    {
        if (x < 0 || x >= viewportWidth) return;
        if (y < 0 || y >= RowHeight) return;
        AddRune(x, y, r);
    }
}
