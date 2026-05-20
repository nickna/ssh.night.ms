using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Views;

// Renders a CellGrid into a Terminal.Gui v2 viewport. Decouples art data from Terminal.Gui:
// the grid uses our own ArtColor/ArtStyle types (see Art/), and the view wraps them into TG
// Attributes at paint time. Use Grid = ... to swap content; the view invalidates itself.
internal sealed class AnsiArtView : View
{
    private CellGrid? _grid;
    private Attribute[]? _attrs;
    private Rune[]? _glyphs;
    private string?[]? _combined;
    private int _width;

    public CellGrid? Grid
    {
        get => _grid;
        set
        {
            _grid = value;
            if (value is not null)
            {
                Width = value.Width;
                Height = value.Height;
                Precompute(value);
            }
            else
            {
                _attrs = null;
                _glyphs = null;
                _combined = null;
                _width = 0;
            }
            SetNeedsDraw();
        }
    }

    public AnsiArtView()
    {
        CanFocus = false;
    }

    // Pre-convert ArtColor → Attribute and bake combined glyph+modifier strings once on
    // assignment so the paint loop is allocation-free in the common case. A typical 80×24
    // banner repaints often (lobby, register, gallery); the old per-cell `new Color(...)`
    // and `glyph.ToString() + mod.ToString()` were the bulk of the per-frame allocations.
    private void Precompute(CellGrid grid)
    {
        var total = grid.Width * grid.Height;
        if (_attrs is null || _attrs.Length != total)
        {
            _attrs = new Attribute[total];
            _glyphs = new Rune[total];
            _combined = new string?[total];
        }
        var attrs = _attrs;
        var glyphs = _glyphs!;
        var combined = _combined!;
        _width = grid.Width;
        for (var y = 0; y < grid.Height; y++)
        {
            for (var x = 0; x < grid.Width; x++)
            {
                var cell = grid[x, y];
                var fg = new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B, 0xFF);
                var bg = new Color(cell.Background.R, cell.Background.G, cell.Background.B, 0xFF);
                var style = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
                var i = y * _width + x;
                attrs[i] = new Attribute(fg, bg, style);
                glyphs[i] = cell.Glyph;
                combined[i] = cell.Modifier is { } mod
                    ? string.Create(cell.Glyph.Utf16SequenceLength + mod.Utf16SequenceLength, (cell.Glyph, mod), static (span, t) =>
                    {
                        t.Glyph.EncodeToUtf16(span);
                        t.mod.EncodeToUtf16(span[t.Glyph.Utf16SequenceLength..]);
                    })
                    : null;
            }
        }
    }

    protected override bool OnDrawingContent(DrawContext? context)
    {
        if (_grid is not { } grid || _attrs is null || _glyphs is null || _combined is null)
            return base.OnDrawingContent(context);

        for (var y = 0; y < grid.Height; y++)
        {
            for (var x = 0; x < grid.Width; x++)
            {
                var i = y * _width + x;
                SetAttribute(_attrs[i]);
                if (_combined[i] is { } s)
                {
                    Move(x, y);
                    AddStr(s);
                }
                else
                {
                    AddRune(x, y, _glyphs[i]);
                }
            }
        }
        return true;
    }
}
