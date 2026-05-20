using System.Text;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem.Events;
using Night.Ms.SshServer.Doors.Games.Slots;
using Night.Ms.SshServer.Tui.Art;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
using Cell = Night.Ms.SshServer.Tui.Art.Cell;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Spatial-table render. Layout reads like a real card table:
//   - viewer is always pinned to bottom-center (PokerStars convention), so seats fan out
//     clockwise from there as _mySeat rotates
//   - top row holds three opponent plaques; bottom row holds two side plaques flanking the
//     viewer's well (centered, full hole-card sprites)
//   - board renders dead-center as colored sprites on a billiard-green felt
//
// Cabinet 78×22, designed to fit inside an 80×24 terminal alongside header + chat (5 rows)
// + action prompt. Plaques are 22 cols × 3 rows; viewer well is centered.
//
// Showdown reveal for opponents uses bold colored text ("A♠ K♥") on the plaque's third row
// — there isn't vertical room for full sprite reveals at the top seats without colliding
// with the board (only 3 rows above it). Markers swap to text in place.
internal sealed class HoldemTableView : View
{
    public const int CabinetWidth = 78;
    public const int CabinetHeight = 22;

    private const int TitleRow = 1;
    private const int PotBannerRow = 7;
    private const int BoardRow = 8;
    private const int ViewerCardsRow = 15;
    private const int ViewerCaptionRow = 20;

    private const int PlaqueWidth = 22;
    private const int TopPlaqueRow = 3;
    private const int SidePlaqueRow = 14;

    private const int TopLeftCol = 1;
    private const int TopCenterCol = (CabinetWidth - PlaqueWidth) / 2;
    private const int TopRightCol = CabinetWidth - PlaqueWidth - 1;
    private const int BottomLeftCol = 1;
    private const int BottomRightCol = CabinetWidth - PlaqueWidth - 1;

    private const int BoardSlots = 5;
    private const int BoardTotalWidth = BoardSlots * CardSprites.Width + (BoardSlots - 1);
    private const int BoardStartCol = (CabinetWidth - BoardTotalWidth) / 2;

    private const int ViewerCardCount = 2;
    private const int ViewerCardsTotalWidth = ViewerCardCount * CardSprites.Width + (ViewerCardCount - 1);
    private const int ViewerCardsStartCol = (CabinetWidth - ViewerCardsTotalWidth) / 2;

    private static readonly Rune TextPresentationSelector = new(0xFE0E);

    private readonly CellGrid _grid = new(CabinetWidth, CabinetHeight);

    private IReadOnlyList<SeatSnapshotDto> _seats = Array.Empty<SeatSnapshotDto>();
    private int _dealerSeat;
    private int? _actorSeat;
    private int _mySeat = -1;
    private long _pot;
    private string _phase = "Idle";
    private int _handNumber;
    private IReadOnlyList<Card> _board = Array.Empty<Card>();
    private string? _myHole1;
    private string? _myHole2;

    public HoldemTableView()
    {
        CanFocus = false;
        Width = CabinetWidth;
        Height = CabinetHeight;
        Repaint();
    }

    public void SetState(
        int handNumber,
        string phase,
        int dealerSeat,
        int? actorSeat,
        long pot,
        IReadOnlyList<string> boardWire,
        IReadOnlyList<SeatSnapshotDto> seats,
        int mySeat,
        string? myHole1,
        string? myHole2)
    {
        _handNumber = handNumber;
        _phase = phase;
        _dealerSeat = dealerSeat;
        _actorSeat = actorSeat;
        _pot = pot;
        _board = boardWire.Select(CardWire.Parse).ToList();
        _seats = seats;
        _mySeat = mySeat;
        _myHole1 = myHole1;
        _myHole2 = myHole2;
        Repaint();
    }

    private void Repaint()
    {
        PaintBackground();
        PaintChrome();
        PaintTitle();
        PaintPotBanner();
        PaintBoard();
        PaintAllPlaques();
        SetNeedsDraw();
    }

    private void PaintBackground()
    {
        var fg = SlotSymbolSprites.Palette.BrightWhite;
        var felt = SlotSymbolSprites.Palette.Felt;
        var black = SlotSymbolSprites.Palette.Black;
        for (var y = 0; y < CabinetHeight; y++)
        {
            for (var x = 0; x < CabinetWidth; x++)
            {
                var inChromeRow = y < 3 || y == CabinetHeight - 1;
                var inChromeCol = x == 0 || x == CabinetWidth - 1;
                var bg = (inChromeRow || inChromeCol) ? black : felt;
                _grid[x, y] = new Cell(new Rune(' '), fg, bg, ArtStyle.None);
            }
        }
    }

    private void PaintChrome()
    {
        var fg = SlotSymbolSprites.Palette.BrightWhite;
        var black = SlotSymbolSprites.Palette.Black;
        SetCell(0, 0, '╔', fg, black);
        SetCell(CabinetWidth - 1, 0, '╗', fg, black);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 0, '═', fg, black);
        SetCell(0, 2, '╠', fg, black);
        SetCell(CabinetWidth - 1, 2, '╣', fg, black);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 2, '═', fg, black);
        SetCell(0, CabinetHeight - 1, '╚', fg, black);
        SetCell(CabinetWidth - 1, CabinetHeight - 1, '╝', fg, black);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, CabinetHeight - 1, '═', fg, black);
        for (var y = 1; y < CabinetHeight - 1; y++)
        {
            if (y == 2) continue;
            SetCell(0, y, '║', fg, black);
            SetCell(CabinetWidth - 1, y, '║', fg, black);
        }
    }

    private void PaintTitle()
    {
        var marquee = " * NIGHT.MS — HOLD'EM * ";
        var subtitle = _handNumber > 0 ? $"Hand #{_handNumber}" : "Waiting…";
        var gold = SlotSymbolSprites.Palette.Gold;
        var dim = SlotSymbolSprites.Palette.DimGray;
        var white = SlotSymbolSprites.Palette.BrightWhite;
        var black = SlotSymbolSprites.Palette.Black;

        DrawText(marquee, 2, TitleRow, gold, black, ArtStyle.Bold);
        DrawText(subtitle, 2 + marquee.Length + 2, TitleRow, white, black, ArtStyle.None);

        var rightInfo = _actorSeat is int a
            ? $"▶ seat{a + 1}   dealer:{_dealerSeat + 1}"
            : $"dealer:{_dealerSeat + 1}";
        if (rightInfo.Length < CabinetWidth - 4)
            DrawText(rightInfo, CabinetWidth - 2 - rightInfo.Length, TitleRow, dim, black, ArtStyle.None);
    }

    private void PaintPotBanner()
    {
        // Centered "── PHASE   POT $X ──" on the felt above the board. Provides a focal
        // point in the felt area so the player's eye lands on phase + pot first.
        var gold = SlotSymbolSprites.Palette.Gold;
        var felt = SlotSymbolSprites.Palette.Felt;
        var phase = string.IsNullOrEmpty(_phase) ? "" : _phase.ToUpperInvariant();
        var pot = _pot > 0 ? $"POT ${_pot}" : "";
        var sb = new StringBuilder("── ");
        if (phase.Length > 0) { sb.Append(phase); if (pot.Length > 0) sb.Append("   "); }
        if (pot.Length > 0) sb.Append(pot);
        sb.Append(" ──");
        var text = sb.ToString();
        var x = Math.Max(1, (CabinetWidth - text.Length) / 2);
        DrawText(text, x, PotBannerRow, gold, felt, ArtStyle.Bold);
    }

    private void PaintBoard()
    {
        // Only paint dealt cards. Empty slots leave felt visible — a real table doesn't
        // show card outlines before the flop, and felt-colored placeholders would just be
        // visual noise.
        for (var i = 0; i < _board.Count && i < BoardSlots; i++)
        {
            BlitSprite(_board[i], CardStyle.Normal, BoardStartCol + i * (CardSprites.Width + 1), BoardRow);
        }
    }

    private void PaintAllPlaques()
    {
        // Spectator (no _mySeat) uses seat 0 as the pivot — fixed orientation prevents the
        // layout from shuffling when other players come and go.
        int? viewer = _mySeat >= 0 ? _mySeat : null;
        for (var i = 0; i < 6; i++)
        {
            var pos = HoldemSeatLayout.MapSeatToPosition(i, viewer);
            var seat = _seats.FirstOrDefault(s => s.SeatIndex == i);
            PaintPlaque(i, seat, pos);
        }
    }

    private void PaintPlaque(int seatIndex, SeatSnapshotDto? seat, HoldemSeatPosition pos)
    {
        if (pos == HoldemSeatPosition.Viewer)
        {
            PaintViewerWell(seatIndex, seat);
            return;
        }

        var (originX, originY) = PlaqueOrigin(pos);
        var felt = SlotSymbolSprites.Palette.Felt;
        var white = SlotSymbolSprites.Palette.BrightWhite;
        var dim = SlotSymbolSprites.Palette.DimGray;
        var gold = SlotSymbolSprites.Palette.Gold;
        var red = SlotSymbolSprites.Palette.CherryRed;

        if (seat is null || seat.Kind == "empty")
        {
            DrawText($"seat{seatIndex + 1}: open", originX, originY, dim, felt, ArtStyle.None);
            return;
        }

        var isActor = seat.SeatIndex == _actorSeat;
        var isDealer = seat.SeatIndex == _dealerSeat;
        var isFolded = string.Equals(seat.Status, "Folded", StringComparison.OrdinalIgnoreCase);
        var isAllIn = string.Equals(seat.Status, "AllIn", StringComparison.OrdinalIgnoreCase);
        var isSittingOut = string.Equals(seat.Status, "SittingOut", StringComparison.OrdinalIgnoreCase);
        var isAwaiting = string.Equals(seat.Status, "AwaitingNextHand", StringComparison.OrdinalIgnoreCase);

        var nameColor = isFolded || isSittingOut || isAwaiting ? dim
            : isActor ? gold
            : isAllIn ? red
            : white;
        var nameStyle = isActor ? ArtStyle.Bold : ArtStyle.None;

        var label = seat.Kind == "cpu" ? $"[{seat.Handle}]" : seat.Handle ?? "?";
        if (label.Length > PlaqueWidth - 4) label = label[..(PlaqueWidth - 4)];
        DrawText(label, originX, originY, nameColor, felt, nameStyle);
        if (isDealer)
            DrawText("●D", originX + PlaqueWidth - 3, originY, gold, felt, ArtStyle.Bold);

        var sb = new StringBuilder();
        sb.Append('$').Append(seat.Stack);
        if (seat.BetThisRound > 0) sb.Append("  bet $").Append(seat.BetThisRound);
        var stackText = sb.ToString();
        if (stackText.Length > PlaqueWidth - 1) stackText = stackText[..(PlaqueWidth - 1)];
        DrawText(stackText, originX, originY + 1, isFolded || isAwaiting ? dim : white, felt, ArtStyle.None);

        // Row 2: hole cards or status pill. Cards swap from "▒▒ ▒▒" markers (in hand,
        // hidden) to bold colored text (showdown reveal). Folded/sitting-out seats show
        // only their pill.
        var statusPill = isFolded ? "[FOLD]" : isAllIn ? "[ALL-IN]" : isSittingOut ? "[OUT]" : "";
        var statusColor = isAllIn ? red : dim;
        var inHand = !isFolded && !isSittingOut && !isAwaiting;

        if (inHand && seat.Hole1 is not null && seat.Hole2 is not null)
        {
            DrawCardText(seat.Hole1, originX, originY + 2, felt);
            DrawCardText(seat.Hole2, originX + 4, originY + 2, felt);
            if (statusPill.Length > 0)
                DrawText(statusPill, originX + 9, originY + 2, statusColor, felt, ArtStyle.Bold);
        }
        else if (inHand)
        {
            DrawText("▒▒ ▒▒", originX, originY + 2, gold, felt, ArtStyle.Bold);
            if (statusPill.Length > 0)
                DrawText(statusPill, originX + 6, originY + 2, statusColor, felt, ArtStyle.Bold);
        }
        else if (statusPill.Length > 0)
        {
            DrawText(statusPill, originX, originY + 2, statusColor, felt, ArtStyle.Bold);
        }
    }

    private void PaintViewerWell(int seatIndex, SeatSnapshotDto? seat)
    {
        var felt = SlotSymbolSprites.Palette.Felt;
        var gold = SlotSymbolSprites.Palette.Gold;
        var white = SlotSymbolSprites.Palette.BrightWhite;
        var dim = SlotSymbolSprites.Palette.DimGray;
        var red = SlotSymbolSprites.Palette.CherryRed;

        if (seat is null || seat.Kind == "empty")
        {
            var text = $"seat{seatIndex + 1}: open";
            var x = Math.Max(1, (CabinetWidth - text.Length) / 2);
            DrawText(text, x, ViewerCaptionRow, dim, felt, ArtStyle.None);
            return;
        }

        var isMe = seatIndex == _mySeat;
        var isActor = seatIndex == _actorSeat;
        var isDealer = seatIndex == _dealerSeat;
        var isFolded = string.Equals(seat.Status, "Folded", StringComparison.OrdinalIgnoreCase);
        var isAllIn = string.Equals(seat.Status, "AllIn", StringComparison.OrdinalIgnoreCase);

        // Hole cards: full sprites when we have them. Spectators viewing the pivot seat
        // won't have private cards — render ▒▒ markers in the same well to keep the
        // layout filled.
        var card1 = isMe ? _myHole1 : seat.Hole1;
        var card2 = isMe ? _myHole2 : seat.Hole2;

        if (card1 is not null && card2 is not null && !isFolded)
        {
            var c1 = CardWire.Parse(card1);
            var c2 = CardWire.Parse(card2);
            BlitSprite(c1, CardStyle.Normal, ViewerCardsStartCol, ViewerCardsRow);
            BlitSprite(c2, CardStyle.Normal, ViewerCardsStartCol + CardSprites.Width + 1, ViewerCardsRow);
        }
        else if (!isFolded)
        {
            var markerText = "▒▒  ▒▒";
            var x = Math.Max(1, (CabinetWidth - markerText.Length) / 2);
            DrawText(markerText, x, ViewerCardsRow + 2, gold, felt, ArtStyle.Bold);
        }

        var sb = new StringBuilder();
        sb.Append(isMe ? "YOU " : seat.Handle ?? "?");
        sb.Append('$').Append(seat.Stack);
        if (seat.BetThisRound > 0) sb.Append("  bet $").Append(seat.BetThisRound);
        if (isDealer) sb.Append("  ●D");
        if (isActor) sb.Append("  ▶ to act");
        if (isAllIn) sb.Append("  [ALL-IN]");
        else if (isFolded) sb.Append("  [FOLD]");
        var caption = sb.ToString();
        var captionColor = isActor ? gold : isAllIn ? red : isFolded ? dim : white;
        var captionX = Math.Max(1, (CabinetWidth - caption.Length) / 2);
        DrawText(caption, captionX, ViewerCaptionRow, captionColor, felt, ArtStyle.Bold);
    }

    private (int x, int y) PlaqueOrigin(HoldemSeatPosition pos) => pos switch
    {
        HoldemSeatPosition.TopLeft => (TopLeftCol, TopPlaqueRow),
        HoldemSeatPosition.TopCenter => (TopCenterCol, TopPlaqueRow),
        HoldemSeatPosition.TopRight => (TopRightCol, TopPlaqueRow),
        HoldemSeatPosition.BottomLeft => (BottomLeftCol, SidePlaqueRow),
        HoldemSeatPosition.BottomRight => (BottomRightCol, SidePlaqueRow),
        HoldemSeatPosition.Viewer => (ViewerCardsStartCol, ViewerCardsRow),
        _ => (1, 3),
    };

    private void DrawCardText(string wireCard, int x, int y, ArtColor bg)
    {
        var card = CardWire.Parse(wireCard);
        var fg = card.Suit is Suit.Hearts or Suit.Diamonds
            ? SlotSymbolSprites.Palette.CherryRed
            : SlotSymbolSprites.Palette.BrightWhite;
        var rank = card.RankLabel;
        DrawText(rank, x, y, fg, bg, ArtStyle.Bold);
        var pipX = x + rank.Length;
        if (pipX < CabinetWidth - 1)
            _grid[pipX, y] = new Cell(new Rune(card.SuitGlyph), fg, bg, ArtStyle.Bold, TextPresentationSelector);
    }

    private void BlitSprite(Card? card, CardStyle style, int x, int y)
    {
        var sprite = CardSprites.Build(card, style);
        for (var dy = 0; dy < CardSprites.Height; dy++)
        {
            for (var dx = 0; dx < CardSprites.Width; dx++)
            {
                var tx = x + dx;
                var ty = y + dy;
                if (tx <= 0 || tx >= CabinetWidth - 1) continue;
                if (ty < 0 || ty >= CabinetHeight) continue;
                _grid[tx, ty] = sprite[dx, dy];
            }
        }
    }

    private void DrawText(string text, int x, int y, ArtColor fg, ArtColor bg, ArtStyle style)
    {
        for (var i = 0; i < text.Length && x + i < CabinetWidth - 1; i++)
            _grid[x + i, y] = new Cell(new Rune(text[i]), fg, bg, style);
    }

    private void SetCell(int x, int y, char glyph, ArtColor fg, ArtColor bg, ArtStyle style = ArtStyle.None)
        => _grid[x, y] = new Cell(new Rune(glyph), fg, bg, style);

    protected override bool OnDrawingContent(DrawContext? context)
    {
        for (var y = 0; y < _grid.Height; y++)
        {
            for (var x = 0; x < _grid.Width; x++)
            {
                var cell = _grid[x, y];
                var fg = new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B, 0xFF);
                var bg = new Color(cell.Background.R, cell.Background.G, cell.Background.B, 0xFF);
                var tgStyle = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
                SetAttribute(new Attribute(fg, bg, tgStyle));
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
