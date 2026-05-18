using System.Text;
using Night.Ms.SshServer.Doors.Games.Common;
using Night.Ms.SshServer.Tui.Art;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
using Cell = Night.Ms.SshServer.Tui.Art.Cell;

namespace Night.Ms.SshServer.Doors.Games.Slots;

// Custom Terminal.Gui View that draws the full slot-machine cabinet: marquee, three reel
// housings, a side handle, and a coin tray. Owns the rendering state (per-reel spin offsets,
// locked symbols, win-flash tier, coin burst). SlotsScreen drives state via SetReelSymbol /
// StartSpinning / AdvanceSpin / SetWinFlash / AddCoinBurst / AdvanceCoinBurst, then calls
// SetNeedsDraw — this view paints by mutating a CellGrid each frame and converting cells to
// Terminal.Gui Attributes inside OnDrawingContent (mirrors AnsiArtView).
//
// Layout (38 cols × 13 rows):
//   row 0   ╔...═...╗                top border
//   row 1   ║   * NIGHT.MS SLOTS *   ║   marquee
//   row 2   ╠...═...╣                separator
//   row 3   ║                       ║   interior
//   row 4   ║   ╔═════╗  ╔═════╗  ╔═════╗  ╔══╗  ║   housing tops + handle top
//   row 5..7 reel content (5×3 sprite per reel) + handle body
//   row 8   ║   ╚═════╝ ...                  ╚══╝  ║   housing bottoms + handle base
//   row 9   ║                       ║   interior
//   row 10  ╠...═...╣                separator
//   row 11  ║   $ coin tray         ║   coin burst paints here on win
//   row 12  ╚...═...╝                bottom border
internal sealed class SlotsCabinetView : View
{
    public const int CabinetWidth = 38;
    public const int CabinetHeight = 13;

    // Inner content area top-left for each reel. Each reel is exactly 5 cols wide × 3 rows
    // tall (matches SlotSymbolSprites.Width × Height).
    private static readonly int[] ReelInnerX = { 4, 13, 22 };
    private const int ReelInnerY = 5;

    // Non-blank symbols in a fixed cycle. Used to build the perceived "scrolling" content
    // while a reel is spinning — each reel walks this list at a phase offset so the three
    // reels never show the same sprite simultaneously.
    private static readonly SlotSymbol[] CycleSymbols =
    {
        SlotSymbol.Cherry,
        SlotSymbol.Lemon,
        SlotSymbol.Plum,
        SlotSymbol.Bell,
        SlotSymbol.Bar,
        SlotSymbol.Seven,
    };

    private readonly CellGrid _grid = new(CabinetWidth, CabinetHeight);
    private readonly bool[] _spinning = new bool[3];
    private readonly int[] _scrollOffset = new int[3];
    private readonly SlotSymbol[] _lockedSymbol = { SlotSymbol.Blank, SlotSymbol.Blank, SlotSymbol.Blank };
    private WinTier _winTier = WinTier.None;
    private int _flashFrame;
    private readonly List<Coin> _coins = new();
    private readonly Random _visualRandom = new();

    private struct Coin
    {
        public int X;
        public int Y;
        public int FramesAlive;
    }

    public SlotsCabinetView()
    {
        CanFocus = false;
        Width = CabinetWidth;
        Height = CabinetHeight;
        RepaintAll();
    }

    public bool HasCoins => _coins.Count > 0;

    public void StartSpinning(int reel)
    {
        _spinning[reel] = true;
        // Phase shift per reel so the three reels show different sprites at the same moment.
        _scrollOffset[reel] = reel * 7;
        RepaintAll();
    }

    public void AdvanceSpin()
    {
        for (var i = 0; i < 3; i++)
            if (_spinning[i]) _scrollOffset[i]++;
        RepaintAll();
    }

    public void LockReel(int reel, SlotSymbol symbol)
    {
        _spinning[reel] = false;
        _lockedSymbol[reel] = symbol;
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
                // Spread across the coin tray inner width, away from the outer border.
                X = _visualRandom.Next(2, CabinetWidth - 2),
                Y = 11,
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
            var coin = _coins[i];
            coin.FramesAlive++;
            if (coin.FramesAlive >= 0 && coin.FramesAlive % 2 == 0)
                coin.Y--;
            // Retire coins that have floated past the top interior row.
            if (coin.Y < 3)
            {
                _coins.RemoveAt(i);
            }
            else
            {
                _coins[i] = coin;
            }
        }
        RepaintAll();
    }

    private void RepaintAll()
    {
        PaintStaticChrome();
        PaintMarquee();
        PaintBorder();
        for (var i = 0; i < 3; i++) PaintReel(i);
        PaintCoins();
        SetNeedsDraw();
    }

    // Paints the cabinet box, separators, reel housings, and handle in default chrome
    // (bright white on black). Win-flash overrides the border afterwards in PaintBorder.
    private void PaintStaticChrome()
    {
        var fg = SlotSymbolSprites.Palette.BrightWhite;
        var bg = SlotSymbolSprites.Palette.Black;

        // Clear interior to spaces on black.
        for (var y = 0; y < CabinetHeight; y++)
            for (var x = 0; x < CabinetWidth; x++)
                _grid[x, y] = new Cell(new Rune(' '), fg, bg, ArtStyle.None);

        // Outer border (rows 0 and 12).
        SetCell(0, 0, '╔', fg);
        SetCell(CabinetWidth - 1, 0, '╗', fg);
        SetCell(0, CabinetHeight - 1, '╚', fg);
        SetCell(CabinetWidth - 1, CabinetHeight - 1, '╝', fg);
        for (var x = 1; x < CabinetWidth - 1; x++)
        {
            SetCell(x, 0, '═', fg);
            SetCell(x, CabinetHeight - 1, '═', fg);
        }
        // Vertical sides.
        for (var y = 1; y < CabinetHeight - 1; y++)
        {
            SetCell(0, y, '║', fg);
            SetCell(CabinetWidth - 1, y, '║', fg);
        }
        // Separators below marquee (row 2) and above coin tray (row 10).
        SetCell(0, 2, '╠', fg);
        SetCell(CabinetWidth - 1, 2, '╣', fg);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 2, '═', fg);
        SetCell(0, 10, '╠', fg);
        SetCell(CabinetWidth - 1, 10, '╣', fg);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 10, '═', fg);

        // Three reel housings (5 inner cols, 3 inner rows). Outer corners at:
        //   reel 1: cols 3..9    reel 2: cols 12..18    reel 3: cols 21..27
        // Each housing spans rows 4 (top), 5..7 (body), 8 (bottom).
        foreach (var leftCol in new[] { 3, 12, 21 })
            PaintHousing(leftCol, 4, 7, 3, fg);

        // Side handle: 4 cols wide × 5 rows tall at cols 31..34, rows 4..8.
        PaintHousing(31, 4, 2, 3, fg);
        // Handle body (gold knob).
        var goldStyle = ArtStyle.Bold;
        for (var y = 5; y <= 7; y++)
        {
            SetCell(32, y, '█', SlotSymbolSprites.Palette.Gold, goldStyle);
            SetCell(33, y, '█', SlotSymbolSprites.Palette.Gold, goldStyle);
        }
    }

    // Paints a rectangular box of inner size (innerCols × innerRows) starting at outer
    // top-left (leftCol, topRow). Uses ╔ ═ ╗ ║ ╚ ╝ in the given chrome color.
    private void PaintHousing(int leftCol, int topRow, int innerCols, int innerRows, ArtColor color)
    {
        var rightCol = leftCol + innerCols + 1;
        var bottomRow = topRow + innerRows + 1;
        SetCell(leftCol, topRow, '╔', color);
        SetCell(rightCol, topRow, '╗', color);
        SetCell(leftCol, bottomRow, '╚', color);
        SetCell(rightCol, bottomRow, '╝', color);
        for (var x = leftCol + 1; x < rightCol; x++)
        {
            SetCell(x, topRow, '═', color);
            SetCell(x, bottomRow, '═', color);
        }
        for (var y = topRow + 1; y < bottomRow; y++)
        {
            SetCell(leftCol, y, '║', color);
            SetCell(rightCol, y, '║', color);
        }
    }

    private void PaintMarquee()
    {
        var gold = SlotSymbolSprites.Palette.Gold;
        var bg = SlotSymbolSprites.Palette.Black;
        const string text = "* NIGHT.MS SLOTS *";
        var startX = (CabinetWidth - text.Length) / 2;
        for (var i = 0; i < text.Length; i++)
            _grid[startX + i, 1] = new Cell(new Rune(text[i]), gold, bg, ArtStyle.Bold);
    }

    private void PaintBorder()
    {
        // Default border = bright white on black (already drawn by PaintStaticChrome). Win
        // flash repaints only the eight border-relevant rows/cols in the cycle color.
        if (_winTier == WinTier.None) return;

        ArtColor color;
        if (_winTier == WinTier.Jackpot)
        {
            // Faster cycle: alternate every frame.
            color = (_flashFrame % 2) == 0
                ? SlotSymbolSprites.Palette.SevenRed
                : SlotSymbolSprites.Palette.Gold;
        }
        else
        {
            // Slower cycle: alternate every two frames.
            color = ((_flashFrame / 2) % 2) == 0
                ? SlotSymbolSprites.Palette.Gold
                : SlotSymbolSprites.Palette.BrightWhite;
        }

        // Repaint outer border + the two ╠═...═╣ separators in the flash color. Glyphs stay
        // the same — only the foreground attribute changes.
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

    // Paints reel `reel` based on its current state. If spinning, paints a 3-row window into
    // an infinite virtual strip of stacked sprites (CycleSymbols repeating). If not spinning,
    // paints the locked symbol's sprite.
    private void PaintReel(int reel)
    {
        var innerX = ReelInnerX[reel];
        if (_spinning[reel])
        {
            var offset = _scrollOffset[reel];
            for (var y = 0; y < SlotSymbolSprites.Height; y++)
            {
                var stripRow = offset + y;
                var symIndex = (stripRow / SlotSymbolSprites.Height) % CycleSymbols.Length;
                if (symIndex < 0) symIndex += CycleSymbols.Length;
                var spriteRow = ((stripRow % SlotSymbolSprites.Height) + SlotSymbolSprites.Height) % SlotSymbolSprites.Height;
                var sprite = SlotSymbolSprites.Get(CycleSymbols[symIndex]);
                for (var x = 0; x < SlotSymbolSprites.Width; x++)
                    _grid[innerX + x, ReelInnerY + y] = sprite[x, spriteRow];
            }
        }
        else
        {
            var sprite = SlotSymbolSprites.Get(_lockedSymbol[reel]);
            for (var y = 0; y < SlotSymbolSprites.Height; y++)
                for (var x = 0; x < SlotSymbolSprites.Width; x++)
                    _grid[innerX + x, ReelInnerY + y] = sprite[x, y];
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
            if (coin.Y < 3 || coin.Y > 11) continue;
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
