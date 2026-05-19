using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Blackjack;

// Mutable state for one round of blackjack. The screen drives this via BlackjackEngine
// (DealInitial, ApplyAction, ...). Engine functions read and mutate this object directly;
// no return values describing changed state, since the screen always inspects the same
// state object after each call.
public sealed class BlackjackGameState
{
    public List<BlackjackHand> PlayerHands { get; } = new();
    public int ActiveIndex { get; set; }
    public List<Card> Dealer { get; } = new();

    // False until the hand transitions to dealer play (or to immediate settlement on a
    // natural / dealer BJ). The view uses this to know whether to paint the hole card.
    public bool DealerRevealed { get; set; }

    // True once all player hands are resolved, dealer has played, and Settle has stamped
    // per-hand outcomes. The screen reads this to know the hand is over and the ledger
    // round can be committed.
    public bool HandComplete { get; set; }

    public Deck Deck { get; }

    public BlackjackGameState(Deck deck)
    {
        Deck = deck;
    }

    public BlackjackHand ActiveHand => PlayerHands[ActiveIndex];

    public int TotalBet => PlayerHands.Sum(h => h.Bet);
    public int TotalPayout => PlayerHands.Sum(h => h.Payout);
}
