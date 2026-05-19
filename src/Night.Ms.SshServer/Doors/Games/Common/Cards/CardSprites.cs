using System.Text;
using Night.Ms.SshServer.Doors.Games.Slots;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Doors.Games.Common.Cards;

internal enum CardStyle
{
    Normal,
    Held,
    Winning,
    Empty,
    FaceDown,
}

// 6×5 colored card sprite shared by every door game. Live styles (Normal / Held / Winning /
// FaceDown) plus an Empty pre-deal slot. Face-up cards render as ink-on-ivory like real
// playing cards: cherry-red hearts/diamonds, near-black clubs/spades on a warm paper body.
// Empty and FaceDown keep the original black-background look (placeholder + dealer hole back).
//
// Layout (cols 0-5, rows 0-4):
//   row 0: ┌────┐   border
//   row 1: │R   │   rank in true upper-left corner (1-char at col 1; "10" spans cols 1-2)
//   row 2: │ S  │   suit pip centered
//   row 3: │   R│   rank in true lower-right corner (1-char at col 4; "10" spans cols 3-4)
//   row 4: └────┘   border
//
// Suit cells carry Cell.Modifier = U+FE0E (text-presentation selector) so the renderer
// emits "♠︎" — forcing single-cell text rendering on clients that would otherwise pick the
// double-wide emoji form for ♠♥♦♣ and break row alignment.
internal static class CardSprites
{
    public const int Width = 6;
    public const int Height = 5;

    private static readonly Rune TextPresentationSelector = new(0xFE0E);

    private static readonly ArtColor Black = SlotSymbolSprites.Palette.Black;
    private static readonly ArtColor Gold = SlotSymbolSprites.Palette.Gold;
    private static readonly ArtColor BarGold = SlotSymbolSprites.Palette.BarGold;
    private static readonly ArtColor Red = SlotSymbolSprites.Palette.CherryRed;
    private static readonly ArtColor DimGray = SlotSymbolSprites.Palette.DimGray;

    // Card-local: warm ivory body and near-black ink. True #000000 can render oddly on some
    // clients against a near-black bg; RGB(20,20,20) reads black to the eye and dodges that.
    private static readonly ArtColor Paper = new(245, 240, 225);
    private static readonly ArtColor Ink = new(20, 20, 20);

    public static Cell[,] Build(Card? card, CardStyle style)
    {
        if (style == CardStyle.FaceDown)
            return BuildFaceDown();
        if (card is null || style == CardStyle.Empty)
            return BuildEmpty();

        var suitColor = card.Suit is Suit.Hearts or Suit.Diamonds ? Red : Ink;
        var rankColor = suitColor;
        var rankStyle = ArtStyle.None;
        var borderColor = Ink;
        var borderStyle = ArtStyle.None;

        switch (style)
        {
            case CardStyle.Held:
                borderColor = BarGold;
                borderStyle = ArtStyle.Bold;
                break;
            case CardStyle.Winning:
                borderColor = Gold;
                borderStyle = ArtStyle.Bold;
                rankStyle = ArtStyle.Bold;
                break;
        }

        var grid = new Cell[Width, Height];
        FillInterior(grid, Ink, Paper);
        DrawBorder(grid, borderColor, Paper, borderStyle);

        var rank = card.RankLabel;
        var pip = card.SuitGlyph;

        if (rank.Length == 1)
        {
            // True diagonal corners: upper-left (1,1) and lower-right (4,3).
            SetGlyph(grid, 1, 1, rank[0], rankColor, Paper, rankStyle);
            SetGlyph(grid, 4, 3, rank[0], rankColor, Paper, rankStyle);
        }
        else
        {
            // "10": top-left spans cols 1-2, bottom-right spans cols 3-4 (rotational mirror).
            SetGlyph(grid, 1, 1, rank[0], rankColor, Paper, rankStyle);
            SetGlyph(grid, 2, 1, rank[1], rankColor, Paper, rankStyle);
            SetGlyph(grid, 3, 3, rank[0], rankColor, Paper, rankStyle);
            SetGlyph(grid, 4, 3, rank[1], rankColor, Paper, rankStyle);
        }

        // Center pip. Bold so it reads even when ranks dominate the corners.
        SetSuit(grid, 2, 2, pip, suitColor, Paper, ArtStyle.Bold);

        return grid;
    }

    private static Cell[,] BuildEmpty()
    {
        var grid = new Cell[Width, Height];
        FillInterior(grid, DimGray, Black);
        DrawBorder(grid, DimGray, Black, ArtStyle.None);
        return grid;
    }

    // Dealer hole card: gold-bordered card with a hatched back.
    private static Cell[,] BuildFaceDown()
    {
        var grid = new Cell[Width, Height];
        FillInterior(grid, Gold, Black);
        DrawBorder(grid, Gold, Black, ArtStyle.None);
        for (var y = 1; y < Height - 1; y++)
            for (var x = 1; x < Width - 1; x++)
                grid[x, y] = new Cell(new Rune('▒'), Gold, Black, ArtStyle.Bold);
        return grid;
    }

    private static void FillInterior(Cell[,] grid, ArtColor fg, ArtColor bg)
    {
        for (var y = 0; y < Height; y++)
            for (var x = 0; x < Width; x++)
                grid[x, y] = new Cell(new Rune(' '), fg, bg, ArtStyle.None);
    }

    private static void DrawBorder(Cell[,] grid, ArtColor color, ArtColor bg, ArtStyle style)
    {
        SetGlyph(grid, 0, 0, '┌', color, bg, style);
        SetGlyph(grid, Width - 1, 0, '┐', color, bg, style);
        SetGlyph(grid, 0, Height - 1, '└', color, bg, style);
        SetGlyph(grid, Width - 1, Height - 1, '┘', color, bg, style);
        for (var x = 1; x < Width - 1; x++)
        {
            SetGlyph(grid, x, 0, '─', color, bg, style);
            SetGlyph(grid, x, Height - 1, '─', color, bg, style);
        }
        for (var y = 1; y < Height - 1; y++)
        {
            SetGlyph(grid, 0, y, '│', color, bg, style);
            SetGlyph(grid, Width - 1, y, '│', color, bg, style);
        }
    }

    private static void SetGlyph(Cell[,] grid, int x, int y, char glyph, ArtColor fg, ArtColor bg, ArtStyle style)
        => grid[x, y] = new Cell(new Rune(glyph), fg, bg, style);

    private static void SetSuit(Cell[,] grid, int x, int y, char pip, ArtColor fg, ArtColor bg, ArtStyle style)
        => grid[x, y] = new Cell(new Rune(pip), fg, bg, style, TextPresentationSelector);
}
