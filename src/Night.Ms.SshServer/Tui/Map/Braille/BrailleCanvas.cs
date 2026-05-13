using System.Text;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Map.Braille;

// 2×4-subpixel-per-cell drawing surface backed by Unicode braille patterns (U+2800–U+28FF).
// One terminal cell carries an 8-bit dot mask, a foreground color (the on-dot color), and a
// background color (the off-dot color). Three paint operations cover the layered map use
// case without needing per-pixel color storage:
//
//   FillCell  — solid colour, used for polygon interiors. Sets mask=0xFF and bg=fg so the
//               cell reads as a uniform block, with hairline braille gaps where the dot
//               geometry doesn't quite tile.
//   SetPixel  — one dot, used for line rasterisation. If the target cell was previously
//               solid-filled, transitions it into "dots-over-fill": the prior fill colour
//               moves to bg, the new dot's colour becomes fg, and the mask resets to just
//               this dot. So a road dot landing in a water cell shows as road-colour-on-blue
//               rather than blowing away the water entirely.
//   DrawText  — overrides the braille glyph with a single text rune for labels; bg is
//               preserved so a label dropped into a water cell still reads as "label on
//               water" instead of floating on black.
//
// Drawing order matters: paint polygons first, then lines, then labels. The class is
// internal-only and not thread-safe — each render builds one canvas single-threaded.
internal sealed class BrailleCanvas
{
    public const int SubPixelsX = 2;
    public const int SubPixelsY = 4;
    private const char BrailleBase = '⠀';

    private readonly byte[,] _mask;       // [row, col]   — 0..0xFF braille dot bits
    private readonly ArtColor[,] _fg;
    private readonly ArtColor[,] _bg;
    private readonly Rune[,] _glyph;      // non-default overrides the braille glyph
    private readonly bool[,] _isFilled;   // true if last paint was FillCell (drives the SetPixel transition)

    public int CellCols { get; }
    public int CellRows { get; }
    public int PixelWidth  => CellCols * SubPixelsX;
    public int PixelHeight => CellRows * SubPixelsY;

    public BrailleCanvas(int cellCols, int cellRows)
    {
        if (cellCols < 0) throw new ArgumentOutOfRangeException(nameof(cellCols));
        if (cellRows < 0) throw new ArgumentOutOfRangeException(nameof(cellRows));
        CellCols = cellCols;
        CellRows = cellRows;
        _mask = new byte[cellRows, cellCols];
        _fg = new ArtColor[cellRows, cellCols];
        _bg = new ArtColor[cellRows, cellCols];
        _glyph = new Rune[cellRows, cellCols];
        _isFilled = new bool[cellRows, cellCols];
    }

    public void Clear(ArtColor background)
    {
        for (var r = 0; r < CellRows; r++)
        {
            for (var c = 0; c < CellCols; c++)
            {
                _mask[r, c] = 0;
                _fg[r, c] = ArtColor.DefaultForeground;
                _bg[r, c] = background;
                _glyph[r, c] = default;
                _isFilled[r, c] = false;
            }
        }
    }

    public void FillCell(int cellX, int cellY, ArtColor color)
    {
        if ((uint)cellX >= (uint)CellCols || (uint)cellY >= (uint)CellRows) return;
        _mask[cellY, cellX] = 0xFF;
        _fg[cellY, cellX] = color;
        _bg[cellY, cellX] = color;
        _glyph[cellY, cellX] = default;
        _isFilled[cellY, cellX] = true;
    }

    public void SetPixel(int subPixelX, int subPixelY, ArtColor color)
    {
        if ((uint)subPixelX >= (uint)PixelWidth || (uint)subPixelY >= (uint)PixelHeight) return;
        var cellX = subPixelX / SubPixelsX;
        var cellY = subPixelY / SubPixelsY;
        var dx = subPixelX - cellX * SubPixelsX;
        var dy = subPixelY - cellY * SubPixelsY;
        var bit = DotBit(dx, dy);

        if (_isFilled[cellY, cellX])
        {
            // Transition out of "solid fill" — preserve the fill colour as the new bg so
            // the dot reads as "fg-coloured dot floating on bg-coloured fill", not a stray
            // pixel on black.
            _bg[cellY, cellX] = _fg[cellY, cellX];
            _mask[cellY, cellX] = bit;
            _fg[cellY, cellX] = color;
            _isFilled[cellY, cellX] = false;
        }
        else
        {
            _mask[cellY, cellX] |= bit;
            _fg[cellY, cellX] = color;
        }
        // Drawing a dot clears any text glyph that was sitting there — labels are painted
        // last in the pipeline so this only fires in pathological ordering.
        _glyph[cellY, cellX] = default;
    }

    public void DrawText(int cellX, int cellY, Rune rune, ArtColor color)
    {
        if ((uint)cellX >= (uint)CellCols || (uint)cellY >= (uint)CellRows) return;
        _glyph[cellY, cellX] = rune;
        _fg[cellY, cellX] = color;
    }

    public CellGrid ToCellGrid()
    {
        var grid = new CellGrid(CellCols, CellRows);
        for (var y = 0; y < CellRows; y++)
        {
            for (var x = 0; x < CellCols; x++)
            {
                Rune glyph;
                if (_glyph[y, x].Value != 0)
                {
                    glyph = _glyph[y, x];
                }
                else
                {
                    var mask = _mask[y, x];
                    glyph = mask == 0
                        ? new Rune(' ')
                        : new Rune(BrailleBase + mask);
                }
                grid[x, y] = new Cell(glyph, _fg[y, x], _bg[y, x], ArtStyle.None);
            }
        }
        return grid;
    }

    // Maps a subpixel (x in 0..1, y in 0..3) to its U+2800 bit position. Layout follows the
    // Unicode block: dots 1-3 stack vertically on the left, dots 4-6 on the right, dots 7-8
    // sit on the fourth row.
    private static byte DotBit(int x, int y)
    {
        if (y < 3) return (byte)(1 << (x * 3 + y));    // x=0 → bits 0,1,2;  x=1 → bits 3,4,5
        return (byte)(1 << (6 + x));                   // y=3 → bit 6 (left) / bit 7 (right)
    }
}
