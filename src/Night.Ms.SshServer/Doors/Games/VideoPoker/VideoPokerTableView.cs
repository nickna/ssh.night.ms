using System.Text;
using Night.Ms.SshServer.Doors.Games.Common;
using Night.Ms.SshServer.Doors.Games.Slots;
using Night.Ms.SshServer.Tui.Art;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
using Cell = Night.Ms.SshServer.Tui.Art.Cell;

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

// Custom Terminal.Gui View that draws the full video-poker table: marquee, five card
// slots, slot indices, "HELD" tags, a 2-column paytable strip, and a coin tray. Mirrors
// SlotsCabinetView architecture (CellGrid + RepaintAll + OnDrawingContent). Owns all
// rendering state; VideoPokerScreen drives it via SetHand / SetHolds / SetShowdown /
// SetWinFlash / AddCoinBurst etc.
//
// Layout (50 cols × 17 rows):
//   row 0    ╔════════════════════════════════════════════════╗
//   row 1    ║      *  NIGHT.MS - VIDEO POKER  *              ║   marquee (banner overlay on win)
//   row 2    ╠════════════════════════════════════════════════╣
//   row 3-7  card sprites (6×5 each) at start cols 8,15,22,29,36
//   row 8    slot indices "1 2 3 4 5" under each card
//   row 9    "HELD" tags (gold) under held cards
//   row 10   ╠════════════════════════════════════════════════╣
//   row 11-15 paytable, two columns
//   row 15   coin tray (coins float upward from here on a win)
//   row 16   ╚════════════════════════════════════════════════╝
internal sealed class VideoPokerTableView : View
{
    public const int CabinetWidth = 50;
    public const int CabinetHeight = 17;

    private static readonly int[] CardStartCol = { 8, 15, 22, 29, 36 };
    private const int CardStartRow = 3;
    private const int IndexRow = 8;
    private const int HeldRow = 9;
    private const int CoinTrayRow = 15;
    private const int CoinTopBound = 3;  // coins retire when they float above this row

    private readonly CellGrid _grid = new(CabinetWidth, CabinetHeight);
    private readonly Card?[] _hand = new Card?[5];
    private readonly bool[] _holds = new bool[5];
    private int[] _winningIndices = Array.Empty<int>();
    private HandRank? _showdownRank;
    private bool _showdownPaid;
    private WinTier _winTier = WinTier.None;
    private int _flashFrame;
    private readonly List<Coin> _coins = new();
    private readonly Random _visualRandom = new();

    // Paytable rows: (rank used for highlight lookup, cabinet row, start col, text).
    // Left column at col 8, right column at col 27. Text widths are pre-padded so the
    // payout values line up vertically.
    private static readonly (HandRank Rank, int Row, int Col, string Text)[] PaytableEntries =
    {
        (HandRank.RoyalFlush,    11,  8, "Royal Flush 250"),
        (HandRank.StraightFlush, 12,  8, "Strt Flush   50"),
        (HandRank.FourOfAKind,   13,  8, "Four Kind    25"),
        (HandRank.FullHouse,     14,  8, "Full House    9"),
        (HandRank.Flush,         15,  8, "Flush         6"),
        (HandRank.Straight,      11, 27, "Straight     4"),
        (HandRank.ThreeOfAKind,  12, 27, "3-Kind       3"),
        (HandRank.TwoPair,       13, 27, "Two Pair     2"),
        (HandRank.JacksOrBetter, 14, 27, "Jacks+       1"),
    };

    private struct Coin
    {
        public int X;
        public int Y;
        public int FramesAlive;
    }

    public VideoPokerTableView()
    {
        CanFocus = false;
        Width = CabinetWidth;
        Height = CabinetHeight;
        RepaintAll();
    }

    public bool HasCoins => _coins.Count > 0;

    public void SetHand(Card?[] hand)
    {
        for (var i = 0; i < 5; i++) _hand[i] = hand[i];
        RepaintAll();
    }

    public void SetHolds(bool[] holds)
    {
        for (var i = 0; i < 5; i++) _holds[i] = holds[i];
        RepaintAll();
    }

    public void SetShowdown(HandRank rank, int[] winningIndices, bool paid)
    {
        _showdownRank = rank;
        _showdownPaid = paid;
        _winningIndices = winningIndices ?? Array.Empty<int>();
        RepaintAll();
    }

    public void ClearShowdown()
    {
        _showdownRank = null;
        _showdownPaid = false;
        _winningIndices = Array.Empty<int>();
        RepaintAll();
    }

    public void SetWinFlash(WinTier tier, int frame)
    {
        _winTier = tier;
        _flashFrame = frame;
        RepaintAll();
    }

    public void ClearWinFlash()
    {
        _winTier = WinTier.None;
        _flashFrame = 0;
        RepaintAll();
    }

    public void AddCoinBurst(int count)
    {
        _coins.Clear();
        for (var i = 0; i < count; i++)
        {
            _coins.Add(new Coin
            {
                // Spread across the cabinet interior, away from the side borders.
                X = _visualRandom.Next(2, CabinetWidth - 2),
                Y = CoinTrayRow,
                // Stagger spawn times so coins don't all rise in lockstep.
                FramesAlive = -_visualRandom.Next(0, 8),
            });
        }
        RepaintAll();
    }

    public void AdvanceCoinBurst()
    {
        for (var i = _coins.Count - 1; i >= 0; i--)
        {
            var c = _coins[i];
            c.FramesAlive++;
            if (c.FramesAlive >= 0 && c.FramesAlive % 2 == 0) c.Y--;
            if (c.Y < CoinTopBound)
                _coins.RemoveAt(i);
            else
                _coins[i] = c;
        }
        RepaintAll();
    }

    private void RepaintAll()
    {
        PaintBackground();
        PaintChrome();
        PaintMarqueeOrBanner();
        PaintCards();
        PaintIndices();
        PaintHelds();
        PaintPaytable();
        PaintBorderFlash();
        PaintCoins();
        SetNeedsDraw();
    }

    private void PaintBackground()
    {
        var fg = SlotSymbolSprites.Palette.BrightWhite;
        var bg = SlotSymbolSprites.Palette.Black;
        for (var y = 0; y < CabinetHeight; y++)
            for (var x = 0; x < CabinetWidth; x++)
                _grid[x, y] = new Cell(new Rune(' '), fg, bg, ArtStyle.None);
    }

    private void PaintChrome()
    {
        var fg = SlotSymbolSprites.Palette.BrightWhite;

        SetCell(0, 0, '╔', fg);
        SetCell(CabinetWidth - 1, 0, '╗', fg);
        SetCell(0, CabinetHeight - 1, '╚', fg);
        SetCell(CabinetWidth - 1, CabinetHeight - 1, '╝', fg);
        for (var x = 1; x < CabinetWidth - 1; x++)
        {
            SetCell(x, 0, '═', fg);
            SetCell(x, CabinetHeight - 1, '═', fg);
        }
        for (var y = 1; y < CabinetHeight - 1; y++)
        {
            SetCell(0, y, '║', fg);
            SetCell(CabinetWidth - 1, y, '║', fg);
        }
        // Separators below the marquee and above the paytable.
        SetCell(0, 2, '╠', fg);
        SetCell(CabinetWidth - 1, 2, '╣', fg);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 2, '═', fg);
        SetCell(0, 10, '╠', fg);
        SetCell(CabinetWidth - 1, 10, '╣', fg);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 10, '═', fg);
    }

    private void PaintMarqueeOrBanner()
    {
        var bg = SlotSymbolSprites.Palette.Black;

        string text;
        ArtColor color;
        if (_showdownRank.HasValue && _showdownPaid)
        {
            text = $"*  {_showdownRank.Value.DisplayName().ToUpperInvariant()}  *";
            color = _showdownRank.Value >= HandRank.StraightFlush
                ? SlotSymbolSprites.Palette.SevenRed
                : SlotSymbolSprites.Palette.Gold;
        }
        else
        {
            text = "*  NIGHT.MS - VIDEO POKER  *";
            color = SlotSymbolSprites.Palette.Gold;
        }

        var startX = (CabinetWidth - text.Length) / 2;
        if (startX < 1) startX = 1;
        for (var i = 0; i < text.Length && startX + i < CabinetWidth - 1; i++)
            _grid[startX + i, 1] = new Cell(new Rune(text[i]), color, bg, ArtStyle.Bold);
    }

    private void PaintCards()
    {
        var winningSet = new HashSet<int>(_winningIndices);
        for (var i = 0; i < 5; i++)
        {
            var card = _hand[i];
            CardStyle style;
            if (card is null) style = CardStyle.Empty;
            else if (winningSet.Contains(i)) style = CardStyle.Winning;
            else if (_holds[i]) style = CardStyle.Held;
            else style = CardStyle.Normal;

            var sprite = CardSprites.Build(card, style);
            var startX = CardStartCol[i];
            for (var y = 0; y < CardSprites.Height; y++)
                for (var x = 0; x < CardSprites.Width; x++)
                    _grid[startX + x, CardStartRow + y] = sprite[x, y];
        }
    }

    private void PaintIndices()
    {
        var color = SlotSymbolSprites.Palette.DimGray;
        var bg = SlotSymbolSprites.Palette.Black;
        for (var i = 0; i < 5; i++)
        {
            var col = CardStartCol[i] + 2;
            _grid[col, IndexRow] = new Cell(new Rune((char)('1' + i)), color, bg, ArtStyle.None);
        }
    }

    private void PaintHelds()
    {
        var gold = SlotSymbolSprites.Palette.BarGold;
        var bg = SlotSymbolSprites.Palette.Black;
        for (var i = 0; i < 5; i++)
        {
            if (!_holds[i]) continue;
            const string label = "HELD";
            var startX = CardStartCol[i] + 1;  // offset 1 inside the 6-wide card slot
            for (var k = 0; k < label.Length; k++)
                _grid[startX + k, HeldRow] = new Cell(new Rune(label[k]), gold, bg, ArtStyle.Bold);
        }
    }

    private void PaintPaytable()
    {
        var bg = SlotSymbolSprites.Palette.Black;
        var normalFg = SlotSymbolSprites.Palette.BrightWhite;

        foreach (var (rank, row, col, text) in PaytableEntries)
        {
            var highlight = _showdownRank == rank;
            var fg = highlight ? SlotSymbolSprites.Palette.Black : normalFg;
            var cellBg = highlight ? SlotSymbolSprites.Palette.Gold : bg;
            var style = highlight ? ArtStyle.Bold : ArtStyle.None;
            for (var i = 0; i < text.Length; i++)
                _grid[col + i, row] = new Cell(new Rune(text[i]), fg, cellBg, style);
        }
    }

    private void PaintBorderFlash()
    {
        if (_winTier == WinTier.None) return;
        ArtColor color;
        if (_winTier == WinTier.Jackpot)
        {
            color = (_flashFrame % 2) == 0
                ? SlotSymbolSprites.Palette.SevenRed
                : SlotSymbolSprites.Palette.Gold;
        }
        else
        {
            color = ((_flashFrame / 2) % 2) == 0
                ? SlotSymbolSprites.Palette.Gold
                : SlotSymbolSprites.Palette.BrightWhite;
        }
        RecolorOuterBorder(color);
    }

    private void RecolorOuterBorder(ArtColor color)
    {
        var bg = SlotSymbolSprites.Palette.Black;
        var style = ArtStyle.Bold;
        for (var x = 0; x < CabinetWidth; x++)
        {
            _grid[x, 0] = new Cell(_grid[x, 0].Glyph, color, bg, style);
            _grid[x, 2] = new Cell(_grid[x, 2].Glyph, color, bg, style);
            _grid[x, 10] = new Cell(_grid[x, 10].Glyph, color, bg, style);
            _grid[x, CabinetHeight - 1] = new Cell(_grid[x, CabinetHeight - 1].Glyph, color, bg, style);
        }
        for (var y = 1; y < CabinetHeight - 1; y++)
        {
            _grid[0, y] = new Cell(_grid[0, y].Glyph, color, bg, style);
            _grid[CabinetWidth - 1, y] = new Cell(_grid[CabinetWidth - 1, y].Glyph, color, bg, style);
        }
    }

    private void PaintCoins()
    {
        var gold = SlotSymbolSprites.Palette.CoinGold;
        var bg = SlotSymbolSprites.Palette.Black;
        foreach (var coin in _coins)
        {
            if (coin.FramesAlive < 0) continue;
            if (coin.X <= 0 || coin.X >= CabinetWidth - 1) continue;
            if (coin.Y < CoinTopBound || coin.Y >= CabinetHeight - 1) continue;
            _grid[coin.X, coin.Y] = new Cell(new Rune('$'), gold, bg, ArtStyle.Bold);
        }
    }

    private void SetCell(int x, int y, char glyph, ArtColor fg, ArtStyle style = ArtStyle.None)
        => _grid[x, y] = new Cell(new Rune(glyph), fg, SlotSymbolSprites.Palette.Black, style);

    protected override bool OnDrawingContent(DrawContext? context)
    {
        for (var y = 0; y < _grid.Height; y++)
        {
            for (var x = 0; x < _grid.Width; x++)
            {
                var cell = _grid[x, y];
                var fg = new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B, 0xFF);
                var bg = new Color(cell.Background.R, cell.Background.G, cell.Background.B, 0xFF);
                var style = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
                SetAttribute(new Attribute(fg, bg, style));
                AddRune(x, y, cell.Glyph);
            }
        }
        return true;
    }
}
