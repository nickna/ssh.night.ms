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
            }
            SetNeedsDraw();
        }
    }

    public AnsiArtView()
    {
        CanFocus = false;
    }

    protected override bool OnDrawingContent(DrawContext? context)
    {
        if (_grid is not { } grid) return base.OnDrawingContent(context);

        for (var y = 0; y < grid.Height; y++)
        {
            for (var x = 0; x < grid.Width; x++)
            {
                var cell = grid[x, y];
                var fg = new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B, 0xFF);
                var bg = new Color(cell.Background.R, cell.Background.G, cell.Background.B, 0xFF);
                var style = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
                SetAttribute(new Attribute(fg, bg, style));
                if (cell.Modifier is { } mod)
                {
                    Move(x, y);
                    AddStr(cell.Glyph.ToString() + mod.ToString());
                }
                else
                {
                    AddRune(x, y, cell.Glyph);
                }
            }
        }
        return true;
    }
}
