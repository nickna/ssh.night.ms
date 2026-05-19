using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// One slot at the table. Index in HoldemTableState.Seats is the absolute seat index. Empty
// seats keep their slot in the list so seat numbering stays stable across hands.
//
// HasOption tracks "must still act this betting round." The round closes when no Active
// seat has HasOption == true (folded/all-in seats are never gating).
public sealed class HoldemSeatState
{
    public HoldemSeatStatus Status { get; set; } = HoldemSeatStatus.Empty;
    public long Stack { get; set; }
    public long BetThisRound { get; set; }
    public long TotalContribution { get; set; }
    public Card? Hole1 { get; set; }
    public Card? Hole2 { get; set; }
    public bool HasOption { get; set; }
}
