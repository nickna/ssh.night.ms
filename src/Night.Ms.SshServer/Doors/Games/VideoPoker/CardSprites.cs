using System.Text;
using Night.Ms.SshServer.Doors.Games.Slots;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

internal enum CardStyle
{
    Normal,
    Held,
    Winning,
    Empty,
}

// 6×5 colored card sprite. Three live styles (Normal / Held / Winning) plus an Empty
// pre-deal slot. The slots palette is reused so both door games share the same casino
// look. Cherry-red hearts/diamonds, white-on-black clubs/spades — chosen because the
// terminal background is black and "real" black would be invisible.
//
// Layout (cols 0-5, rows 0-4):
//   row 0: ┌────┐   border
//   row 1: │ R..│   rank top-left  (1-char ranks at col 2; "10" spans cols 2-3)
//   row 2: │ S  │   suit letter at col 2
//   row 3: │..R │   rank bottom-right (1-char at col 4; "10" spans cols 3-4)
//   row 4: └────┘   border
internal static class CardSprites
{
    public const int Width = 6;
    public const int Height = 5;

    private static readonly ArtColor Black = SlotSymbolSprites.Palette.Black;
    private static readonly ArtColor White = SlotSymbolSprites.Palette.BrightWhite;
    private static readonly ArtColor Gold = SlotSymbolSprites.Palette.Gold;
    private static readonly ArtColor BarGold = SlotSymbolSprites.Palette.BarGold;
    private static readonly ArtColor Red = SlotSymbolSprites.Palette.CherryRed;
    private static readonly ArtColor DimGray = SlotSymbolSprites.Palette.DimGray;

    public static Cell[,] Build(Card? card, CardStyle style)
    {
        if (card is null || style == CardStyle.Empty)
            return BuildEmpty();

        ArtColor borderColor;
        ArtColor rankColor;
        var borderStyle = ArtStyle.None;
        var rankStyle = ArtStyle.None;

        switch (style)
        {
            case CardStyle.Held:
                borderColor = BarGold;
                rankColor = White;
                break;
            case CardStyle.Winning:
                borderColor = Gold;
                rankColor = Gold;
                borderStyle = ArtStyle.Bold;
                rankStyle = ArtStyle.Bold;
                break;
            default:
                borderColor = White;
                rankColor = White;
                break;
        }

        var suitColor = card.Suit is Suit.Hearts or Suit.Diamonds ? Red : White;

        var grid = new Cell[Width, Height];
        FillInterior(grid, White);
        DrawBorder(grid, borderColor, borderStyle);

        var rank = card.RankLabel;
        var suit = card.SuitGlyph[0];

        if (rank.Length == 1)
        {
            SetGlyph(grid, 2, 1, rank[0], rankColor, rankStyle);
            SetGlyph(grid, 4, 3, rank[0], rankColor, rankStyle);
        }
        else
        {
            // "10": top-left occupies cols 2-3, bottom-right cols 3-4 (mirrors corner pip pattern).
            SetGlyph(grid, 2, 1, rank[0], rankColor, rankStyle);
            SetGlyph(grid, 3, 1, rank[1], rankColor, rankStyle);
            SetGlyph(grid, 3, 3, rank[0], rankColor, rankStyle);
            SetGlyph(grid, 4, 3, rank[1], rankColor, rankStyle);
        }

        SetGlyph(grid, 2, 2, suit, suitColor, ArtStyle.None);

        return grid;
    }

    private static Cell[,] BuildEmpty()
    {
        var grid = new Cell[Width, Height];
        FillInterior(grid, DimGray);
        DrawBorder(grid, DimGray, ArtStyle.None);
        return grid;
    }

    private static void FillInterior(Cell[,] grid, ArtColor fg)
    {
        for (var y = 0; y < Height; y++)
            for (var x = 0; x < Width; x++)
                grid[x, y] = new Cell(new Rune(' '), fg, Black, ArtStyle.None);
    }

    private static void DrawBorder(Cell[,] grid, ArtColor color, ArtStyle style)
    {
        SetGlyph(grid, 0, 0, '┌', color, style);
        SetGlyph(grid, Width - 1, 0, '┐', color, style);
        SetGlyph(grid, 0, Height - 1, '└', color, style);
        SetGlyph(grid, Width - 1, Height - 1, '┘', color, style);
        for (var x = 1; x < Width - 1; x++)
        {
            SetGlyph(grid, x, 0, '─', color, style);
            SetGlyph(grid, x, Height - 1, '─', color, style);
        }
        for (var y = 1; y < Height - 1; y++)
        {
            SetGlyph(grid, 0, y, '│', color, style);
            SetGlyph(grid, Width - 1, y, '│', color, style);
        }
    }

    private static void SetGlyph(Cell[,] grid, int x, int y, char glyph, ArtColor fg, ArtStyle style)
        => grid[x, y] = new Cell(new Rune(glyph), fg, Black, style);
}
