using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Blackjack;

// One player hand at the table. The original deal is a single BlackjackHand; splitting
// creates a second alongside it. Bet is the *current* wager on this hand — Double mutates
// Bet in place so the audit row's per-hand bet reflects what was actually risked.
public sealed class BlackjackHand
{
    public List<Card> Cards { get; } = new();
    public int Bet { get; set; }
    public bool Doubled { get; set; }
    public bool FromSplit { get; set; }
    public bool FromSplitAces { get; set; }
    public bool Resolved { get; set; }
    public BlackjackResult? Result { get; set; }
    public int Payout { get; set; }
}
