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

// Custom-paint table view. Cohesive with the slots cabinet and the blackjack table:
// double-bordered chrome, gold marquee, palette-matched card sprites. Picks a focused
// rendering strategy — the *board* is the most attention-grabbing surface in Hold'em,
// so it gets painted as colored card sprites; per-seat data renders as compact text
// rows so all six seats fit in the available height.
//
// Layout (76 cols × 13 rows):
//   row 0       ╔══════════════════════════════════════════════════════════════════════════╗
//   row 1       ║ * NIGHT.MS — HOLD'EM * — Hand #N — Pot $X — Phase: Flop ║
//   row 2       ╠══════════════════════════════════════════════════════════════════════════╣
//   rows 3-8    six seat rows (one line each, viewer marked with *)
//   rows 9-13   board sprite area (5 rows tall)
//   bottom      no bottom border — chat pane below provides visual closure
internal sealed class HoldemTableView : View
{
    public const int CabinetWidth = 76;
    public const int CabinetHeight = 14;

    private const int TitleRow = 1;
    private const int SeatsStartRow = 3;
    private const int SeatRowCount = 6;     // exactly MaxSeats
    private const int BoardRow = 9;

    private readonly CellGrid _grid = new(CabinetWidth, CabinetHeight);

    // State (set via SetState; read by Paint*). Mutations + reads always happen on the
    // UI thread because HoldemScreen marshals through _app.Invoke.
    private string _title = string.Empty;
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
        PaintSeats();
        PaintBoard();
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
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 0, '═', fg);
        for (var y = 1; y < CabinetHeight; y++)
        {
            SetCell(0, y, '║', fg);
            SetCell(CabinetWidth - 1, y, '║', fg);
        }
        // Separator below title.
        SetCell(0, 2, '╠', fg);
        SetCell(CabinetWidth - 1, 2, '╣', fg);
        for (var x = 1; x < CabinetWidth - 1; x++) SetCell(x, 2, '═', fg);
    }

    private void PaintTitle()
    {
        var marquee = " * NIGHT.MS — HOLD'EM * ";
        var subtitle = _handNumber > 0
            ? $"Hand #{_handNumber} — Pot ${_pot} — {_phase}"
            : "Waiting for next hand…";
        var combined = $"{marquee}  {subtitle}";
        var gold = SlotSymbolSprites.Palette.Gold;
        var dim = SlotSymbolSprites.Palette.DimGray;

        // Marquee in gold, subtitle in dim text — drawn separately so each gets its own color.
        DrawText(marquee, 2, TitleRow, gold, ArtStyle.Bold);
        DrawText(subtitle, 2 + marquee.Length + 2, TitleRow, dim, ArtStyle.None);

        // Right-side: current actor + dealer marker. Helps players see at-a-glance who's up.
        var rightInfo = _actorSeat is int a
            ? $"dealer:{_dealerSeat + 1}  to-act:{a + 1}"
            : $"dealer:{_dealerSeat + 1}";
        if (rightInfo.Length < CabinetWidth - 4)
            DrawText(rightInfo, CabinetWidth - 2 - rightInfo.Length, TitleRow, dim, ArtStyle.None);
    }

    private void PaintSeats()
    {
        // Stable 6-row block. Empty slots still take a row so the layout doesn't jitter
        // as players come and go.
        var white = SlotSymbolSprites.Palette.BrightWhite;
        var dim = SlotSymbolSprites.Palette.DimGray;
        var gold = SlotSymbolSprites.Palette.Gold;
        var red = SlotSymbolSprites.Palette.CherryRed;

        for (var i = 0; i < SeatRowCount; i++)
        {
            var row = SeatsStartRow + i;
            var seat = _seats.FirstOrDefault(s => s.SeatIndex == i);
            if (seat is null || seat.Kind == "empty")
            {
                DrawText($"  seat{i + 1}: (empty)", 2, row, dim, ArtStyle.None);
                continue;
            }

            var isMe = seat.SeatIndex == _mySeat;
            var isActor = seat.SeatIndex == _actorSeat;
            var isDealer = seat.SeatIndex == _dealerSeat;
            var isFolded = string.Equals(seat.Status, "Folded", StringComparison.OrdinalIgnoreCase);
            var isAllIn = string.Equals(seat.Status, "AllIn", StringComparison.OrdinalIgnoreCase);
            var isSittingOut = string.Equals(seat.Status, "SittingOut", StringComparison.OrdinalIgnoreCase);

            var color = isFolded || isSittingOut ? dim
                : isActor ? gold
                : isAllIn ? red
                : white;
            var style = (isMe || isActor) ? ArtStyle.Bold : ArtStyle.None;

            var sb = new StringBuilder();
            sb.Append(isMe ? " *" : "  ");
            sb.Append($"seat{seat.SeatIndex + 1}");
            sb.Append(isDealer ? " (D)" : "    ");
            sb.Append(' ');
            var label = seat.Kind == "cpu" ? $"[{seat.Handle}]" : seat.Handle ?? "?";
            sb.Append(label.Length > 16 ? label[..16] : label.PadRight(16));
            sb.Append($"  ${seat.Stack}".PadRight(10));
            if (seat.BetThisRound > 0) sb.Append($"  bet ${seat.BetThisRound}".PadRight(14));
            else sb.Append(new string(' ', 14));
            sb.Append("  ").Append(seat.Status);
            if (isActor) sb.Append("  ◀── to act");

            // Render the line, then if it's the viewer or showdown, overlay their hole-card text.
            DrawText(sb.ToString(), 2, row, color, style);

            // For the viewer's row, show hole cards inline.
            if (isMe && _myHole1 is not null && _myHole2 is not null)
            {
                var cardText = $"  {_myHole1} {_myHole2}";
                var x = Math.Min(CabinetWidth - 1 - cardText.Length, 2 + sb.Length);
                DrawText(cardText, x, row, gold, ArtStyle.Bold);
            }
            // For other seats at showdown, the snapshot may have hole1/hole2 populated.
            else if (seat.Hole1 is not null && seat.Hole2 is not null)
            {
                var cardText = $"  {seat.Hole1} {seat.Hole2}";
                var x = Math.Min(CabinetWidth - 1 - cardText.Length, 2 + sb.Length);
                DrawText(cardText, x, row, gold, ArtStyle.Bold);
            }
        }
    }

    private void PaintBoard()
    {
        // Five card slots centered on the board row. Undealt slots render as Empty
        // sprites (gray with a thin border), dealt ones as live colored sprites.
        const int slots = 5;
        var totalWidth = slots * CardSprites.Width + (slots - 1);
        var startX = (CabinetWidth - totalWidth) / 2;
        for (var i = 0; i < slots; i++)
        {
            Card? card = i < _board.Count ? _board[i] : null;
            var style = card is not null ? CardStyle.Normal : CardStyle.Empty;
            BlitSprite(card, style, startX + i * (CardSprites.Width + 1), BoardRow);
        }
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

    private void DrawText(string text, int x, int y, ArtColor fg, ArtStyle style)
    {
        var bg = SlotSymbolSprites.Palette.Black;
        for (var i = 0; i < text.Length && x + i < CabinetWidth - 1; i++)
            _grid[x + i, y] = new Cell(new System.Text.Rune(text[i]), fg, bg, style);
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
