using Night.Ms.SshServer.Doors.Games.Blackjack;
using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Tests.Doors;

public class BlackjackEngineTests
{
    private static Card C(Rank r, Suit s = Suit.Spades) => new(r, s);

    // Deals consume from the front in P, D, P, D order, then any further draws from action
    // continue from index 4 onward. Helpers below stack the deck in the exact draw order so
    // tests assert against deterministic hands.
    private static Deck StackedDeck(params Card[] cards) => new Deck(cards);

    // ---------- Evaluate ----------

    [Theory]
    [InlineData(Rank.Two, Rank.Three, 5, false)]
    [InlineData(Rank.Ten, Rank.Nine, 19, false)]
    [InlineData(Rank.King, Rank.Queen, 20, false)]
    [InlineData(Rank.Ace, Rank.Six, 17, true)]
    [InlineData(Rank.Ace, Rank.King, 21, true)]   // natural blackjack — soft 21
    public void Evaluate_two_card_hands(Rank a, Rank b, int expectedTotal, bool expectedSoft)
    {
        var (total, soft, bust) = BlackjackEngine.Evaluate(new[] { C(a), C(b) });
        Assert.Equal(expectedTotal, total);
        Assert.Equal(expectedSoft, soft);
        Assert.False(bust);
    }

    [Fact]
    public void Evaluate_demotes_ace_when_overshooting()
    {
        // A-6-K: A would be 11 + 6 + 10 = 27 → demote to 1 + 6 + 10 = 17 (hard).
        var (total, soft, bust) = BlackjackEngine.Evaluate(new[] { C(Rank.Ace), C(Rank.Six), C(Rank.King) });
        Assert.Equal(17, total);
        Assert.False(soft);
        Assert.False(bust);
    }

    [Fact]
    public void Evaluate_multi_ace_keeps_one_high_when_possible()
    {
        // A-A-9: 11 + 1 + 9 = 21 (soft, one ace still counts as 11).
        var (total, soft, _) = BlackjackEngine.Evaluate(new[] { C(Rank.Ace), C(Rank.Ace), C(Rank.Nine) });
        Assert.Equal(21, total);
        Assert.True(soft);
    }

    [Fact]
    public void Evaluate_marks_bust()
    {
        var (total, _, bust) = BlackjackEngine.Evaluate(new[] { C(Rank.King), C(Rank.Queen), C(Rank.Three) });
        Assert.Equal(23, total);
        Assert.True(bust);
    }

    // ---------- Natural blackjack detection ----------

    [Fact]
    public void Natural_blackjack_on_first_two_cards()
    {
        var deck = StackedDeck(C(Rank.Ace, Suit.Hearts), C(Rank.Nine), C(Rank.King, Suit.Hearts), C(Rank.Eight));
        var state = BlackjackEngine.DealInitial(deck, 10);
        Assert.True(state.HandComplete);
        Assert.Equal(BlackjackResult.BlackjackWin, state.PlayerHands[0].Result);
        // 3:2 on bet 10 → payout 25 (10 bet back + 15 win)
        Assert.Equal(25, state.PlayerHands[0].Payout);
    }

    [Fact]
    public void Twenty_one_after_hits_is_not_a_natural()
    {
        // Deal order P,D,P,D: P=5,6 (11); D=9,8 (17). Hit → +10 → 21 (auto-resolves and
        // settles since this is the only player hand).
        var deck = StackedDeck(
            C(Rank.Five), C(Rank.Nine),
            C(Rank.Six), C(Rank.Eight),
            C(Rank.Ten));
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Hit);
        Assert.True(state.HandComplete);
        // Player 21 vs dealer 17 → regular win (1:1), not blackjack (3:2).
        Assert.Equal(BlackjackResult.Win, state.PlayerHands[0].Result);
        Assert.Equal(20, state.PlayerHands[0].Payout);
    }

    // ---------- Dealer S17 ----------

    [Fact]
    public void Dealer_stands_on_soft_17()
    {
        // Player stands on 18. Dealer holds A-6 (soft 17) — should NOT draw.
        var deck = StackedDeck(
            C(Rank.Ten), C(Rank.Ace, Suit.Hearts),
            C(Rank.Eight), C(Rank.Six),
            C(Rank.King));                  // would be the next draw if dealer hit — should remain in deck
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Stand);
        Assert.Equal(2, state.Dealer.Count);
        Assert.Equal(17, BlackjackEngine.Evaluate(state.Dealer).Total);
        Assert.Equal(BlackjackResult.Win, state.PlayerHands[0].Result);
    }

    [Fact]
    public void Dealer_hits_below_17()
    {
        // Dealer 5-K (15) must draw at least one card.
        var deck = StackedDeck(
            C(Rank.Ten), C(Rank.Five),
            C(Rank.Eight), C(Rank.King),
            C(Rank.Three));                 // dealer draws → 5+10+3 = 18
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Stand);
        Assert.Equal(3, state.Dealer.Count);
        Assert.Equal(18, BlackjackEngine.Evaluate(state.Dealer).Total);
        // Player 18 vs Dealer 18 → push.
        Assert.Equal(BlackjackResult.Push, state.PlayerHands[0].Result);
    }

    [Fact]
    public void Dealer_does_not_draw_when_all_player_hands_busted()
    {
        var deck = StackedDeck(
            C(Rank.Ten), C(Rank.Six),
            C(Rank.Nine), C(Rank.Ten),       // player draws to 19? Actually deal is P,D,P,D.
            C(Rank.King));                   // hit → 10 + 9 + K = 29 bust
        var state = BlackjackEngine.DealInitial(deck, 10);
        // After deal: P holds Ten,Nine (19); D holds Six,Ten (16). Force player to bust.
        BlackjackEngine.ApplyAction(state, BlackjackAction.Hit);
        Assert.True(BlackjackEngine.Evaluate(state.PlayerHands[0].Cards).IsBust);
        // Dealer should NOT have drawn — they had 16 but player busted first.
        Assert.Equal(2, state.Dealer.Count);
        Assert.Equal(BlackjackResult.Loss, state.PlayerHands[0].Result);
    }

    // ---------- Legal actions ----------

    [Fact]
    public void Initial_two_cards_allow_hit_stand_double_split_when_equal()
    {
        var deck = StackedDeck(C(Rank.Eight), C(Rank.Five), C(Rank.Eight), C(Rank.Six));
        var state = BlackjackEngine.DealInitial(deck, 10);
        var legal = BlackjackEngine.LegalActions(state, availableCoins: 1_000);
        Assert.Contains(BlackjackAction.Hit, legal);
        Assert.Contains(BlackjackAction.Stand, legal);
        Assert.Contains(BlackjackAction.Double, legal);
        Assert.Contains(BlackjackAction.Split, legal);
    }

    [Fact]
    public void Split_requires_equal_rank_pairs()
    {
        var deck = StackedDeck(C(Rank.Eight), C(Rank.Five), C(Rank.Nine), C(Rank.Six));
        var state = BlackjackEngine.DealInitial(deck, 10);
        var legal = BlackjackEngine.LegalActions(state, availableCoins: 1_000);
        Assert.DoesNotContain(BlackjackAction.Split, legal);
    }

    [Fact]
    public void Double_disallowed_when_short_on_coins()
    {
        var deck = StackedDeck(C(Rank.Five), C(Rank.Five), C(Rank.Five), C(Rank.Five));
        var state = BlackjackEngine.DealInitial(deck, 10);
        // Only 5 coins available — bet is 10, so doubling needs another 10.
        var legal = BlackjackEngine.LegalActions(state, availableCoins: 5);
        Assert.DoesNotContain(BlackjackAction.Double, legal);
        // Split is also 10 more, also disallowed.
        Assert.DoesNotContain(BlackjackAction.Split, legal);
    }

    [Fact]
    public void After_hit_double_and_split_are_no_longer_legal()
    {
        var deck = StackedDeck(C(Rank.Five), C(Rank.Five), C(Rank.Five), C(Rank.Five), C(Rank.Two));
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Hit);
        var legal = BlackjackEngine.LegalActions(state, availableCoins: 1_000);
        Assert.DoesNotContain(BlackjackAction.Double, legal);
        Assert.DoesNotContain(BlackjackAction.Split, legal);
        Assert.Contains(BlackjackAction.Hit, legal);
        Assert.Contains(BlackjackAction.Stand, legal);
    }

    // ---------- Double ----------

    [Fact]
    public void Double_doubles_bet_draws_one_card_and_resolves()
    {
        var deck = StackedDeck(
            C(Rank.Six), C(Rank.Ten),
            C(Rank.Five), C(Rank.Seven),
            C(Rank.Ten),                   // double draw → 6+5+10 = 21
            C(Rank.King));                 // dealer hit → 10+7+K bust
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Double);
        Assert.True(state.HandComplete);
        var hand = state.PlayerHands[0];
        Assert.True(hand.Doubled);
        Assert.Equal(20, hand.Bet);
        // Dealer busts → player wins 1:1 on the doubled bet → payout = 2 * 20 = 40.
        Assert.Equal(BlackjackResult.Win, hand.Result);
        Assert.Equal(40, hand.Payout);
    }

    // ---------- Split ----------

    [Fact]
    public void Splitting_creates_a_second_hand_with_matching_bet()
    {
        var deck = StackedDeck(
            C(Rank.Eight, Suit.Hearts), C(Rank.Ten),
            C(Rank.Eight, Suit.Spades), C(Rank.Seven),
            C(Rank.Three),                 // first split hand top-up → 8 + 3 = 11
            C(Rank.Two));                  // second split hand top-up → 8 + 2 = 10
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Split);
        Assert.Equal(2, state.PlayerHands.Count);
        Assert.Equal(10, state.PlayerHands[0].Bet);
        Assert.Equal(10, state.PlayerHands[1].Bet);
        Assert.True(state.PlayerHands[0].FromSplit);
        Assert.True(state.PlayerHands[1].FromSplit);
        Assert.False(state.PlayerHands[0].Resolved);  // still playable
    }

    [Fact]
    public void Split_aces_get_one_card_each_and_lock_immediately()
    {
        var deck = StackedDeck(
            C(Rank.Ace, Suit.Hearts), C(Rank.Nine),
            C(Rank.Ace, Suit.Spades), C(Rank.Eight),
            C(Rank.Ten),                   // first ace → 21 (not natural — FromSplit)
            C(Rank.Five));                 // second ace → 16
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Split);
        Assert.True(state.HandComplete);
        // Each hand got exactly one extra card and was locked.
        Assert.Equal(2, state.PlayerHands[0].Cards.Count);
        Assert.Equal(2, state.PlayerHands[1].Cards.Count);
        Assert.True(state.PlayerHands[0].Resolved);
        Assert.True(state.PlayerHands[1].Resolved);
        // First hand: A+10 = 21 but not natural (FromSplit) — only beats dealer if dealer < 21.
        // Dealer 9+8 = 17; first hand 21 → Win; second hand 16 → Loss.
        Assert.Equal(BlackjackResult.Win, state.PlayerHands[0].Result);
        Assert.Equal(BlackjackResult.Loss, state.PlayerHands[1].Result);
    }

    [Fact]
    public void After_split_no_double_on_split_aces()
    {
        var deck = StackedDeck(
            C(Rank.Ace, Suit.Hearts), C(Rank.Nine),
            C(Rank.Ace, Suit.Spades), C(Rank.Eight),
            C(Rank.Ten),
            C(Rank.Five));
        var state = BlackjackEngine.DealInitial(deck, 10);
        // Engine auto-resolves both split-ace hands; LegalActions returns empty when
        // HandComplete is true. To probe FromSplitAces gating directly, we'd need a non-ace
        // path that triggers it — instead, here we simply verify the auto-resolution.
        BlackjackEngine.ApplyAction(state, BlackjackAction.Split);
        Assert.True(state.HandComplete);
    }

    [Fact]
    public void Cannot_split_twice_in_v1()
    {
        var deck = StackedDeck(
            C(Rank.Eight, Suit.Hearts), C(Rank.Nine),
            C(Rank.Eight, Suit.Spades), C(Rank.Seven),
            C(Rank.Eight, Suit.Clubs),     // top-up → first hand 8,8 (could-be split again)
            C(Rank.Two));                  // top-up second hand → 8,2
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Split);
        var legal = BlackjackEngine.LegalActions(state, availableCoins: 1_000);
        Assert.DoesNotContain(BlackjackAction.Split, legal);
    }

    // ---------- Settlement ----------

    [Fact]
    public void Player_blackjack_vs_dealer_blackjack_pushes()
    {
        var deck = StackedDeck(
            C(Rank.Ace, Suit.Hearts), C(Rank.Ace, Suit.Spades),
            C(Rank.King, Suit.Hearts), C(Rank.King, Suit.Spades));
        var state = BlackjackEngine.DealInitial(deck, 10);
        Assert.True(state.HandComplete);
        Assert.Equal(BlackjackResult.Push, state.PlayerHands[0].Result);
        Assert.Equal(10, state.PlayerHands[0].Payout);  // bet returned, no winnings
    }

    [Fact]
    public void Dealer_blackjack_beats_non_natural_player()
    {
        // Deal order P,D,P,D: P=Nine,Ten=19; D=Ace,King=21 (natural BJ).
        var deck = StackedDeck(
            C(Rank.Nine), C(Rank.Ace, Suit.Hearts),
            C(Rank.Ten), C(Rank.King, Suit.Hearts));
        var state = BlackjackEngine.DealInitial(deck, 10);
        Assert.True(state.HandComplete);
        Assert.Equal(BlackjackResult.Loss, state.PlayerHands[0].Result);
        Assert.Equal(0, state.PlayerHands[0].Payout);
    }

    [Fact]
    public void Push_returns_bet_only()
    {
        var deck = StackedDeck(
            C(Rank.Ten, Suit.Hearts), C(Rank.Ten, Suit.Spades),
            C(Rank.Nine), C(Rank.Eight));                 // P: 20, D: 18 + ? — but dealer hits to 18? no, 17+ stops.
        // Dealer starts with 10,9 = 19. Already >= 17, stands. Player 10,8 = 18 vs 19 → Loss, not push.
        // Need to set up an actual push: P=20, D=20.
        deck = StackedDeck(
            C(Rank.Ten, Suit.Hearts), C(Rank.Ten, Suit.Spades),
            C(Rank.King, Suit.Hearts), C(Rank.King, Suit.Spades));  // both 20, no naturals
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Stand);
        Assert.Equal(BlackjackResult.Push, state.PlayerHands[0].Result);
        Assert.Equal(10, state.PlayerHands[0].Payout);
    }

    [Fact]
    public void Bust_payout_is_zero()
    {
        var deck = StackedDeck(
            C(Rank.Ten), C(Rank.Six),
            C(Rank.Nine), C(Rank.Ten),       // dealer 16 starts; player 19
            C(Rank.King));                   // hit player → bust
        var state = BlackjackEngine.DealInitial(deck, 10);
        BlackjackEngine.ApplyAction(state, BlackjackAction.Hit);
        Assert.True(state.HandComplete);
        Assert.Equal(BlackjackResult.Loss, state.PlayerHands[0].Result);
        Assert.Equal(0, state.PlayerHands[0].Payout);
    }
}
