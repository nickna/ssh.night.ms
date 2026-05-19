using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Blackjack;

// Pure game logic for v1 blackjack. Rules: single freshly-shuffled deck per hand,
// dealer stands on soft 17, blackjack pays 3:2. Player may hit / stand / double / split.
// Splitting is allowed once (no re-split → max 2 player hands). Split aces receive one
// card each and are auto-resolved. No insurance, no surrender in v1.
//
// All methods are static. State mutation happens on the BlackjackGameState passed in;
// callers re-inspect the same object after each call. Tests can drive a full hand by
// preloading a deterministic deck and calling DealInitial / ApplyAction in sequence.
public static class BlackjackEngine
{
    public const int Target = 21;
    public const int DealerStandsOn = 17;   // S17: dealer stands on any 17, soft or hard.
    public const int MaxHands = 2;          // single split, no re-splits.

    // Blackjack pays 3:2 — the bet must be even for integer winnings, so all callers
    // should constrain bet to multiples of 2. BlackjackGame uses a step of 10 to keep
    // 3:2 trivially clean at every legal bet.
    public static int BlackjackValue(Rank r) => r switch
    {
        Rank.Ace => 11,
        Rank.Jack or Rank.Queen or Rank.King => 10,
        _ => (int)r,
    };

    // Returns the best-but-not-bust total if possible. IsSoft is true when at least one
    // ace is still counted as 11 in the returned total.
    public static (int Total, bool IsSoft, bool IsBust) Evaluate(IReadOnlyList<Card> cards)
    {
        int total = 0, aces = 0;
        foreach (var c in cards)
        {
            total += BlackjackValue(c.Rank);
            if (c.Rank == Rank.Ace) aces++;
        }
        // Demote aces from 11 → 1 one at a time until we're under 21 or out of aces.
        while (total > Target && aces > 0)
        {
            total -= 10;
            aces--;
        }
        return (total, aces > 0, total > Target);
    }

    // A "natural" blackjack: exactly the dealt two-card 21. 21 reached after hits, or
    // 21 on either side of a split, does not count.
    public static bool IsNaturalBlackjack(BlackjackHand hand)
    {
        if (hand.FromSplit) return false;
        if (hand.Cards.Count != 2) return false;
        return Evaluate(hand.Cards).Total == Target;
    }

    public static BlackjackGameState DealInitial(Deck deck, int bet)
    {
        var state = new BlackjackGameState(deck);
        var hand = new BlackjackHand { Bet = bet };

        // Deal P, D, P, D — order doesn't matter for fairness with a pre-shuffled deck,
        // but matches what the player sees on a real table.
        hand.Cards.Add(deck.Draw());
        state.Dealer.Add(deck.Draw());
        hand.Cards.Add(deck.Draw());
        state.Dealer.Add(deck.Draw());
        state.PlayerHands.Add(hand);

        // Naturals and dealer BJ resolve the hand immediately, before any actions.
        var dealerBJ = state.Dealer.Count == 2 && Evaluate(state.Dealer).Total == Target;
        var playerBJ = IsNaturalBlackjack(hand);
        if (dealerBJ || playerBJ)
        {
            hand.Resolved = true;
            state.DealerRevealed = true;
            Settle(state);
            state.HandComplete = true;
        }
        return state;
    }

    // What can the active hand do right now? Empty list if the round is over or the
    // active hand is already resolved. availableCoins is the player's spendable balance
    // *after* the initial bet was already debited at deal time, so the comparison gates
    // Double / Split on whether the player can afford the *additional* wager.
    public static IReadOnlyList<BlackjackAction> LegalActions(BlackjackGameState state, long availableCoins)
    {
        var actions = new List<BlackjackAction>(4);
        if (state.HandComplete) return actions;
        var hand = state.ActiveHand;
        if (hand.Resolved) return actions;

        actions.Add(BlackjackAction.Stand);

        var (total, _, _) = Evaluate(hand.Cards);
        if (total < Target) actions.Add(BlackjackAction.Hit);

        var firstAction = hand.Cards.Count == 2 && !hand.Doubled;
        var canAffordExtra = availableCoins >= hand.Bet;

        if (firstAction && !hand.FromSplitAces && canAffordExtra)
        {
            actions.Add(BlackjackAction.Double);
        }

        if (firstAction
            && state.PlayerHands.Count < MaxHands
            && BlackjackValue(hand.Cards[0].Rank) == BlackjackValue(hand.Cards[1].Rank)
            && canAffordExtra)
        {
            actions.Add(BlackjackAction.Split);
        }

        return actions;
    }

    public static void ApplyAction(BlackjackGameState state, BlackjackAction action)
    {
        if (state.HandComplete) throw new InvalidOperationException("Hand already complete.");
        var hand = state.ActiveHand;
        if (hand.Resolved) throw new InvalidOperationException("Active hand already resolved.");

        switch (action)
        {
            case BlackjackAction.Hit:
                hand.Cards.Add(state.Deck.Draw());
                if (Evaluate(hand.Cards).Total >= Target)
                {
                    // 21 auto-stands (no benefit to hitting again); bust also stops play.
                    hand.Resolved = true;
                    AdvanceOrFinish(state);
                }
                break;

            case BlackjackAction.Stand:
                hand.Resolved = true;
                AdvanceOrFinish(state);
                break;

            case BlackjackAction.Double:
                hand.Bet *= 2;
                hand.Doubled = true;
                hand.Cards.Add(state.Deck.Draw());
                hand.Resolved = true;
                AdvanceOrFinish(state);
                break;

            case BlackjackAction.Split:
                if (state.PlayerHands.Count >= MaxHands)
                    throw new InvalidOperationException("Cannot split: max hands reached.");
                if (BlackjackValue(hand.Cards[0].Rank) != BlackjackValue(hand.Cards[1].Rank))
                    throw new InvalidOperationException("Cannot split: ranks don't match.");

                var isAces = hand.Cards[0].Rank == Rank.Ace;
                var second = hand.Cards[1];
                hand.Cards.RemoveAt(1);
                var newHand = new BlackjackHand { Bet = hand.Bet, FromSplit = true };
                newHand.Cards.Add(second);
                hand.FromSplit = true;

                if (isAces)
                {
                    hand.FromSplitAces = true;
                    newHand.FromSplitAces = true;
                }

                hand.Cards.Add(state.Deck.Draw());
                newHand.Cards.Add(state.Deck.Draw());
                state.PlayerHands.Add(newHand);

                if (isAces)
                {
                    // Split-aces get one card each and are immediately locked.
                    hand.Resolved = true;
                    newHand.Resolved = true;
                    AdvanceOrFinish(state);
                }
                break;
        }
    }

    private static void AdvanceOrFinish(BlackjackGameState state)
    {
        while (state.ActiveIndex < state.PlayerHands.Count
               && state.PlayerHands[state.ActiveIndex].Resolved)
        {
            state.ActiveIndex++;
        }
        if (state.ActiveIndex >= state.PlayerHands.Count)
        {
            PlayDealer(state);
            Settle(state);
            state.HandComplete = true;
        }
    }

    public static void PlayDealer(BlackjackGameState state)
    {
        state.DealerRevealed = true;

        // If every player hand busted, the dealer wins by default without drawing — the
        // outcome is already determined.
        if (state.PlayerHands.All(h => Evaluate(h.Cards).IsBust)) return;

        while (true)
        {
            var (total, _, _) = Evaluate(state.Dealer);
            if (total >= DealerStandsOn) break;
            state.Dealer.Add(state.Deck.Draw());
        }
    }

    public static void Settle(BlackjackGameState state)
    {
        var dealerEval = Evaluate(state.Dealer);
        var dealerBJ = state.Dealer.Count == 2 && dealerEval.Total == Target;

        foreach (var hand in state.PlayerHands)
        {
            var handEval = Evaluate(hand.Cards);
            var playerBJ = IsNaturalBlackjack(hand);

            BlackjackResult outcome;
            int payout;  // total credit returned to wallet — bet back + winnings; 0 on loss.

            if (playerBJ && dealerBJ) { outcome = BlackjackResult.Push; payout = hand.Bet; }
            else if (playerBJ) { outcome = BlackjackResult.BlackjackWin; payout = hand.Bet + (hand.Bet * 3 / 2); }
            else if (dealerBJ) { outcome = BlackjackResult.Loss; payout = 0; }
            else if (handEval.IsBust) { outcome = BlackjackResult.Loss; payout = 0; }
            else if (dealerEval.IsBust) { outcome = BlackjackResult.Win; payout = hand.Bet * 2; }
            else if (handEval.Total > dealerEval.Total) { outcome = BlackjackResult.Win; payout = hand.Bet * 2; }
            else if (handEval.Total < dealerEval.Total) { outcome = BlackjackResult.Loss; payout = 0; }
            else { outcome = BlackjackResult.Push; payout = hand.Bet; }

            hand.Result = outcome;
            hand.Payout = payout;
        }
    }
}
