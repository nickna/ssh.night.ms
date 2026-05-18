using System.Text;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Doors.Games.Slots;

// Static 5×3 colored sprites per SlotSymbol, plus the cabinet palette. Sprites are baked at
// module-load time so the cabinet view never allocates a Cell[,] during a spin.
//
// Glyphs are restricted to ASCII + the half-block / box-drawing ranges already used by the
// art pipeline (▀ ▄ █ ▌ ▐ ╔ ═ ╗ ║ ╚ ╝ ╠ ╣ ★). All are single-column on every SSH client
// we target, so each sprite is exactly 5 cells wide.
internal static class SlotSymbolSprites
{
    public const int Width = 5;
    public const int Height = 3;

    public static class Palette
    {
        public static readonly ArtColor Black = new(0, 0, 0);
        public static readonly ArtColor BrightWhite = new(220, 220, 220);
        public static readonly ArtColor Gold = new(255, 215, 0);
        public static readonly ArtColor BarGold = new(255, 195, 40);
        public static readonly ArtColor SevenRed = new(255, 40, 50);
        public static readonly ArtColor BellYellow = new(255, 230, 90);
        public static readonly ArtColor LemonYellow = new(240, 240, 95);
        public static readonly ArtColor PlumMagenta = new(200, 80, 200);
        public static readonly ArtColor CherryRed = new(220, 40, 60);
        public static readonly ArtColor CherryStem = new(60, 180, 60);
        public static readonly ArtColor DimGray = new(95, 95, 95);
        public static readonly ArtColor CoinGold = new(255, 215, 0);
    }

    // Sprite layout per symbol. '#' = primary color cell (the symbol's fg color on black).
    // '.' = empty (space on black). Cherry uses 'r' for red cells and 'g' for green stem.
    // The 'B', 'A', 'R' letters in Bar render as literal letters in bold gold.
    // Half-block runes (▀ ▄ █) are inline in the strings.
    //
    // Patterns are 3 lines × 5 cols. They're converted into Cell[5,3] arrays at module init.
    private const string SevenPattern =
        "▄▄▄▄▄" +
        "   ▄▀" +
        " ▄▀  ";

    private const string BarPattern =
        "█████" +
        " BAR " +
        "█████";

    private const string BellPattern =
        " ▄█▄ " +
        "█████" +
        " ▀█▀ ";

    private const string PlumPattern =
        " ▄█▄ " +
        "▐███▌" +
        " ▀█▀ ";

    private const string LemonPattern =
        " ▄▀▄ " +
        "▌   ▐" +
        " ▀▄▀ ";

    // Cherry is two-color: red dots + green stem. The "r" cells render in red,
    // "g" cells in green, anything else is empty.
    private const string CherryPattern =
        "  g  " +
        "rr rr" +
        "rr rr";

    private const string BlankPattern =
        "     " +
        "     " +
        "     ";

    private static readonly IReadOnlyDictionary<SlotSymbol, Cell[,]> _sprites =
        new Dictionary<SlotSymbol, Cell[,]>
        {
            [SlotSymbol.Seven] = Build(SevenPattern, Palette.SevenRed, bold: true),
            [SlotSymbol.Bar]   = Build(BarPattern,   Palette.BarGold,  bold: true),
            [SlotSymbol.Bell]  = Build(BellPattern,  Palette.BellYellow, bold: true),
            [SlotSymbol.Plum]  = Build(PlumPattern,  Palette.PlumMagenta),
            [SlotSymbol.Lemon] = Build(LemonPattern, Palette.LemonYellow),
            [SlotSymbol.Cherry] = BuildCherry(),
            [SlotSymbol.Blank] = Build(BlankPattern, Palette.DimGray),
        };

    public static Cell[,] Get(SlotSymbol symbol) => _sprites[symbol];

    // Single-color sprite: ' ' becomes empty, anything else paints the given fg on black.
    // The given fg also paints to bold when bold==true. Half-block runes carry their own
    // shape so the foreground color is what the player perceives as the symbol body.
    private static Cell[,] Build(string pattern, ArtColor fg, bool bold = false)
    {
        if (pattern.Length != Width * Height)
            throw new ArgumentException($"Pattern must be {Width * Height} chars, got {pattern.Length}.", nameof(pattern));

        var style = bold ? ArtStyle.Bold : ArtStyle.None;
        var grid = new Cell[Width, Height];
        for (var y = 0; y < Height; y++)
        {
            for (var x = 0; x < Width; x++)
            {
                var ch = pattern[y * Width + x];
                grid[x, y] = ch == ' '
                    ? new Cell(new Rune(' '), Palette.BrightWhite, Palette.Black, ArtStyle.None)
                    : new Cell(new Rune(ch), fg, Palette.Black, style);
            }
        }
        return grid;
    }

    private static Cell[,] BuildCherry()
    {
        var grid = new Cell[Width, Height];
        for (var y = 0; y < Height; y++)
        {
            for (var x = 0; x < Width; x++)
            {
                var ch = CherryPattern[y * Width + x];
                grid[x, y] = ch switch
                {
                    'r' => new Cell(new Rune('█'), Palette.CherryRed, Palette.Black, ArtStyle.Bold),
                    'g' => new Cell(new Rune('▌'), Palette.CherryStem, Palette.Black, ArtStyle.Bold),
                    _   => new Cell(new Rune(' '), Palette.BrightWhite, Palette.Black, ArtStyle.None),
                };
            }
        }
        return grid;
    }
}
