using System.Text;
using Night.Ms.SshServer.Doors.Games.Common;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Slots;
using Night.Ms.SshServer.Tui.Art;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
using Cell = Night.Ms.SshServer.Tui.Art.Cell;

namespace Night.Ms.SshServer.Doors.Games.Blackjack;

// Custom Terminal.Gui View for the blackjack table. Renders the dealer row, one or two
// player hand rows (single or split), per-hand totals, active-hand indicator, border
// flash on win, and a coin-shower animation. Cohesive with the slots cabinet and the
// video-poker table — same chrome, palette, and coin shower.
//
// Layout (60 cols × 17 rows):
//   row 0    ╔════════════════════════════════════════════════════════════╗
//   row 1    ║                *  NIGHT.MS - BLACKJACK  *                  ║
//   row 2    ╠════════════════════════════════════════════════════════════╣
//   row 3    DEALER label
//   row 4-8  dealer cards (5 rows of card sprite)
//   row 9    player label(s) — single or split
//   row 10-14 player card(s)
//   row 15   coin tray
//   row 16   ╚════════════════════════════════════════════════════════════╝
internal sealed class BlackjackTableView : View
{
    public const int CabinetWidth = 60;
    public const int CabinetHeight = 17;

    private const int DealerLabelRow = 3;
    private const int DealerCardRow = 4;
    private const int PlayerLabelRow = 9;
    private const int PlayerCardRow = 10;
    private const int CoinTrayRow = 15;
    private const int CoinTopBound = 3;

    private const int MaxVisibleSingleRow = 5;
    private const int MaxVisibleSplitPane = 4;

    private readonly CellGrid _grid = new(CabinetWidth, CabinetHeight);

    private IReadOnlyList<Card> _dealer = Array.Empty<Card>();
    private bool _holeRevealed;
    private IReadOnlyList<BlackjackHand> _hands = Array.Empty<BlackjackHand>();
    private int _activeIndex;
    private bool _handComplete;
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

    public BlackjackTableView()
    {
        CanFocus = false;
        Width = CabinetWidth;
        Height = CabinetHeight;
        RepaintAll();
    }

    public bool HasCoins => _coins.Count > 0;

    public void SetState(BlackjackGameState? state)
    {
        if (state is null)
        {
            _dealer = Array.Empty<Card>();
            _hands = Array.Empty<BlackjackHand>();
            _activeIndex = 0;
            _holeRevealed = false;
            _handComplete = false;
        }
        else
        {
            _dealer = state.Dealer.ToArray();
            _hands = state.PlayerHands.ToArray();
            _activeIndex = state.ActiveIndex;
            _holeRevealed = state.DealerRevealed;
            _handComplete = state.HandComplete;
        }
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
                X = _visualRandom.Next(2, CabinetWidth - 2),
                Y = CoinTrayRow,
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
        PaintMarquee();
        PaintDealer();
        PaintPlayer();
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
                _grid[x, y] = new Cell(new System.Text.Rune(' '), fg, bg, ArtStyle.None);
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
        // Separator below the marquee.
        SetCell(0, 2, '╠', fg);
        SetCell(CabinetWidth - 1, 2, '╣', fg);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 2, '═', fg);
    }

    private void PaintMarquee()
    {
        var bg = SlotSymbolSprites.Palette.Black;
        const string text = "*  NIGHT.MS - BLACKJACK  *";
        var color = SlotSymbolSprites.Palette.Gold;
        var startX = (CabinetWidth - text.Length) / 2;
        if (startX < 1) startX = 1;
        for (var i = 0; i < text.Length && startX + i < CabinetWidth - 1; i++)
            _grid[startX + i, 1] = new Cell(new System.Text.Rune(text[i]), color, bg, ArtStyle.Bold);
    }

    private void PaintDealer()
    {
        var labelColor = SlotSymbolSprites.Palette.DimGray;
        var label = BuildDealerLabel();
        DrawText(label, 2, DealerLabelRow, labelColor, ArtStyle.Bold);

        var dealerCards = _dealer.ToList();
        var visible = Math.Min(dealerCards.Count, MaxVisibleSingleRow);
        var startCol = CenteredStartCol(visible);
        for (var i = 0; i < visible; i++)
        {
            var style = (i == 1 && !_holeRevealed) ? CardStyle.FaceDown : CardStyle.Normal;
            BlitCard(dealerCards[i], style, startCol + i * (CardSprites.Width + 1), DealerCardRow);
        }
    }

    private string BuildDealerLabel()
    {
        if (_dealer.Count == 0) return "DEALER";
        if (!_holeRevealed)
        {
            var (total, isSoft, _) = BlackjackEngine.Evaluate(new[] { _dealer[0] });
            return $"DEALER  showing {(isSoft ? "soft " : "")}{total}";
        }
        var eval = BlackjackEngine.Evaluate(_dealer);
        var prefix = (eval.IsSoft && !eval.IsBust) ? "soft " : "";
        var suffix = eval.IsBust ? "  BUST" : "";
        return $"DEALER  {prefix}{eval.Total}{suffix}";
    }

    private void PaintPlayer()
    {
        var labelDim = SlotSymbolSprites.Palette.DimGray;
        var labelDefault = SlotSymbolSprites.Palette.BrightWhite;
        var labelActive = SlotSymbolSprites.Palette.Gold;

        if (_hands.Count == 0)
        {
            DrawText("YOU", 2, PlayerLabelRow, labelDim, ArtStyle.None);
            return;
        }

        if (_hands.Count == 1)
        {
            var hand = _hands[0];
            DrawText(BuildHandLabel(hand, "YOU"), 2, PlayerLabelRow, labelDefault, ArtStyle.Bold);
            var visible = Math.Min(hand.Cards.Count, MaxVisibleSingleRow);
            var startCol = CenteredStartCol(visible);
            for (var i = 0; i < visible; i++)
            {
                BlitCard(hand.Cards[i], ResolveCardStyle(hand), startCol + i * (CardSprites.Width + 1), PlayerCardRow);
            }
            return;
        }

        // Split: two hands side by side in two equal panes.
        for (var h = 0; h < _hands.Count && h < 2; h++)
        {
            var hand = _hands[h];
            var paneStartCol = h == 0 ? 2 : (CabinetWidth / 2 + 1);
            var paneWidth = (CabinetWidth / 2) - 2;

            bool isActive = !_handComplete && h == _activeIndex && !hand.Resolved;
            ArtColor labelColor;
            if (_handComplete) labelColor = labelDefault;
            else if (isActive) labelColor = labelActive;
            else if (hand.Resolved) labelColor = labelDim;
            else labelColor = labelDefault;

            var prefix = isActive ? "> " : "  ";
            DrawText(prefix + BuildHandLabel(hand, $"HAND {h + 1}"),
                     paneStartCol, PlayerLabelRow, labelColor, ArtStyle.Bold);

            var visible = Math.Min(hand.Cards.Count, MaxVisibleSplitPane);
            var totalWidth = visible * CardSprites.Width + (visible - 1);
            var startCol = paneStartCol + Math.Max(0, (paneWidth - totalWidth) / 2);
            for (var i = 0; i < visible; i++)
            {
                BlitCard(hand.Cards[i], ResolveCardStyle(hand), startCol + i * (CardSprites.Width + 1), PlayerCardRow);
            }
        }
    }

    private CardStyle ResolveCardStyle(BlackjackHand hand)
    {
        if (_handComplete && hand.Result is BlackjackResult.Win or BlackjackResult.BlackjackWin)
            return CardStyle.Winning;
        return CardStyle.Normal;
    }

    private string BuildHandLabel(BlackjackHand hand, string title)
    {
        var sb = new StringBuilder();
        sb.Append(title);
        sb.Append("  ");

        var natural = BlackjackEngine.IsNaturalBlackjack(hand);
        var eval = BlackjackEngine.Evaluate(hand.Cards);

        if (natural) sb.Append("BLACKJACK");
        else if (eval.IsBust) sb.Append("BUST");
        else
        {
            if (eval.IsSoft) sb.Append("soft ");
            sb.Append(eval.Total);
        }

        if (hand.Doubled) sb.Append("  DBL");
        sb.Append("  ($").Append(hand.Bet).Append(')');

        if (_handComplete && hand.Result is { } r)
        {
            sb.Append("  — ").Append(r.DisplayName().ToUpperInvariant());
        }
        return sb.ToString();
    }

    private int CenteredStartCol(int visibleCount)
    {
        if (visibleCount <= 0) visibleCount = 1;
        var totalWidth = visibleCount * CardSprites.Width + (visibleCount - 1);
        return Math.Max(1, (CabinetWidth - totalWidth) / 2);
    }

    private void BlitCard(Card? card, CardStyle style, int x, int y)
    {
        var sprite = CardSprites.Build(card, style);
        for (var dy = 0; dy < CardSprites.Height; dy++)
        {
            for (var dx = 0; dx < CardSprites.Width; dx++)
            {
                var tx = x + dx;
                var ty = y + dy;
                if (tx <= 0 || tx >= CabinetWidth - 1) continue;
                if (ty <= 0 || ty >= CabinetHeight - 1) continue;
                _grid[tx, ty] = sprite[dx, dy];
            }
        }
    }

    private void DrawText(string text, int x, int y, ArtColor fg, ArtStyle style)
    {
        var bg = SlotSymbolSprites.Palette.Black;
        for (var i = 0; i < text.Length && x + i < CabinetWidth - 1; i++)
            _grid[x + i, y] = new Cell(new System.Text.Rune(text[i]), fg, bg, style);
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
            _grid[coin.X, coin.Y] = new Cell(new System.Text.Rune('$'), gold, bg, ArtStyle.Bold);
        }
    }

    private void SetCell(int x, int y, char glyph, ArtColor fg, ArtStyle style = ArtStyle.None)
        => _grid[x, y] = new Cell(new System.Text.Rune(glyph), fg, SlotSymbolSprites.Palette.Black, style);

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
