using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem.Evaluator;

namespace Night.Ms.SshServer.Tests.Doors.Games.Holdem;

// Distinct test class name (HoldemHandEvaluatorTests) so it doesn't collide with the
// existing single-deck VideoPoker evaluator tests at tests/Doors/HandEvaluatorTests.cs.
public class HoldemHandEvaluatorTests
{
    private static Card C(Rank r, Suit s) => new(r, s);

    private static List<Card> Hand(params (Rank r, Suit s)[] cards) =>
        cards.Select(t => C(t.r, t.s)).ToList();

    [Fact]
    public void RoyalFlush_is_straight_flush_high_ace()
    {
        var hand = Hand(
            (Rank.Ten, Suit.Spades), (Rank.Jack, Suit.Spades), (Rank.Queen, Suit.Spades),
            (Rank.King, Suit.Spades), (Rank.Ace, Suit.Spades),
            (Rank.Two, Suit.Clubs), (Rank.Three, Suit.Diamonds));
        var v = HandEvaluator.EvaluateBest(hand);
        Assert.Equal(HandCategory.StraightFlush, v.Category);
        Assert.Equal(14, v.T1);
    }

    [Fact]
    public void StraightFlush_higher_top_card_wins()
    {
        var nineHigh = Hand(
            (Rank.Five, Suit.Hearts), (Rank.Six, Suit.Hearts), (Rank.Seven, Suit.Hearts),
            (Rank.Eight, Suit.Hearts), (Rank.Nine, Suit.Hearts),
            (Rank.King, Suit.Clubs), (Rank.Ace, Suit.Diamonds));
        var tenHigh = Hand(
            (Rank.Six, Suit.Hearts), (Rank.Seven, Suit.Hearts), (Rank.Eight, Suit.Hearts),
            (Rank.Nine, Suit.Hearts), (Rank.Ten, Suit.Hearts),
            (Rank.King, Suit.Clubs), (Rank.Ace, Suit.Diamonds));

        Assert.True(HandEvaluator.EvaluateBest(tenHigh) > HandEvaluator.EvaluateBest(nineHigh));
    }

    [Fact]
    public void Quads_kicker_resolves_tie()
    {
        var withAce = Hand(
            (Rank.King, Suit.Spades), (Rank.King, Suit.Hearts), (Rank.King, Suit.Diamonds),
            (Rank.King, Suit.Clubs), (Rank.Ace, Suit.Spades),
            (Rank.Two, Suit.Clubs), (Rank.Three, Suit.Diamonds));
        var withQueen = Hand(
            (Rank.King, Suit.Spades), (Rank.King, Suit.Hearts), (Rank.King, Suit.Diamonds),
            (Rank.King, Suit.Clubs), (Rank.Queen, Suit.Spades),
            (Rank.Two, Suit.Clubs), (Rank.Three, Suit.Diamonds));

        var withAceValue = HandEvaluator.EvaluateBest(withAce);
        var withQueenValue = HandEvaluator.EvaluateBest(withQueen);

        Assert.Equal(HandCategory.FourOfAKind, withAceValue.Category);
        Assert.True(withAceValue > withQueenValue);
    }

    [Fact]
    public void FullHouse_higher_trips_wins_over_higher_pair()
    {
        var ninesFullOfAces = Hand(
            (Rank.Nine, Suit.Spades), (Rank.Nine, Suit.Hearts), (Rank.Nine, Suit.Diamonds),
            (Rank.Ace, Suit.Spades), (Rank.Ace, Suit.Hearts),
            (Rank.Two, Suit.Clubs), (Rank.Three, Suit.Diamonds));
        var tensFullOfTwos = Hand(
            (Rank.Ten, Suit.Spades), (Rank.Ten, Suit.Hearts), (Rank.Ten, Suit.Diamonds),
            (Rank.Two, Suit.Spades), (Rank.Two, Suit.Hearts),
            (Rank.Four, Suit.Clubs), (Rank.Five, Suit.Diamonds));

        Assert.True(HandEvaluator.EvaluateBest(tensFullOfTwos) > HandEvaluator.EvaluateBest(ninesFullOfAces));
    }

    [Fact]
    public void Flush_kicker_order_matters_to_T5()
    {
        // Both ace-jack-nine-five-X flushes; differ only on the last kicker.
        var withThree = Hand(
            (Rank.Ace, Suit.Hearts), (Rank.Jack, Suit.Hearts), (Rank.Nine, Suit.Hearts),
            (Rank.Five, Suit.Hearts), (Rank.Three, Suit.Hearts),
            (Rank.King, Suit.Clubs), (Rank.Two, Suit.Diamonds));
        var withTwo = Hand(
            (Rank.Ace, Suit.Hearts), (Rank.Jack, Suit.Hearts), (Rank.Nine, Suit.Hearts),
            (Rank.Five, Suit.Hearts), (Rank.Two, Suit.Hearts),
            (Rank.King, Suit.Clubs), (Rank.Three, Suit.Diamonds));

        Assert.True(HandEvaluator.EvaluateBest(withThree) > HandEvaluator.EvaluateBest(withTwo));
    }

    [Fact]
    public void Wheel_straight_is_five_high()
    {
        var wheel = Hand(
            (Rank.Ace, Suit.Spades), (Rank.Two, Suit.Hearts), (Rank.Three, Suit.Diamonds),
            (Rank.Four, Suit.Clubs), (Rank.Five, Suit.Spades),
            (Rank.King, Suit.Clubs), (Rank.Queen, Suit.Diamonds));
        var v = HandEvaluator.EvaluateBest(wheel);
        Assert.Equal(HandCategory.Straight, v.Category);
        Assert.Equal(5, v.T1);
    }

    [Fact]
    public void TwoPair_higher_pair_dominates()
    {
        var acesAndTwos = Hand(
            (Rank.Ace, Suit.Spades), (Rank.Ace, Suit.Hearts),
            (Rank.Two, Suit.Diamonds), (Rank.Two, Suit.Clubs),
            (Rank.King, Suit.Spades), (Rank.Seven, Suit.Hearts), (Rank.Three, Suit.Diamonds));
        var kingsAndQueens = Hand(
            (Rank.King, Suit.Spades), (Rank.King, Suit.Hearts),
            (Rank.Queen, Suit.Diamonds), (Rank.Queen, Suit.Clubs),
            (Rank.Ace, Suit.Spades), (Rank.Seven, Suit.Hearts), (Rank.Three, Suit.Diamonds));

        // Despite KQ having higher second pair, AA beats KK on first tiebreaker.
        Assert.True(HandEvaluator.EvaluateBest(acesAndTwos) > HandEvaluator.EvaluateBest(kingsAndQueens));
    }

    [Fact]
    public void OnePair_three_kickers_resolve_tie()
    {
        var akQ = Hand(
            (Rank.Seven, Suit.Spades), (Rank.Seven, Suit.Hearts),
            (Rank.Ace, Suit.Diamonds), (Rank.King, Suit.Clubs), (Rank.Queen, Suit.Spades),
            (Rank.Three, Suit.Hearts), (Rank.Two, Suit.Diamonds));
        var akJ = Hand(
            (Rank.Seven, Suit.Spades), (Rank.Seven, Suit.Hearts),
            (Rank.Ace, Suit.Diamonds), (Rank.King, Suit.Clubs), (Rank.Jack, Suit.Spades),
            (Rank.Three, Suit.Hearts), (Rank.Two, Suit.Diamonds));

        Assert.True(HandEvaluator.EvaluateBest(akQ) > HandEvaluator.EvaluateBest(akJ));
    }

    [Fact]
    public void Board_plays_when_hole_cards_lose_to_community()
    {
        // Board makes a royal flush in spades; the hole cards are irrelevant.
        var hand = Hand(
            (Rank.Two, Suit.Hearts), (Rank.Three, Suit.Hearts),
            (Rank.Ten, Suit.Spades), (Rank.Jack, Suit.Spades), (Rank.Queen, Suit.Spades),
            (Rank.King, Suit.Spades), (Rank.Ace, Suit.Spades));
        var v = HandEvaluator.EvaluateBest(hand);
        Assert.Equal(HandCategory.StraightFlush, v.Category);
        Assert.Equal(14, v.T1);
    }

    [Fact]
    public void DoublePairedBoard_makes_full_house_from_set_plus_pair()
    {
        // Hole: 9d 9c. Board: 9h 2s 2d 7c 5h. Best 5: 9-9-9-2-2 (nines full of twos).
        var hand = Hand(
            (Rank.Nine, Suit.Diamonds), (Rank.Nine, Suit.Clubs),
            (Rank.Nine, Suit.Hearts), (Rank.Two, Suit.Spades), (Rank.Two, Suit.Diamonds),
            (Rank.Seven, Suit.Clubs), (Rank.Five, Suit.Hearts));
        var v = HandEvaluator.EvaluateBest(hand);
        Assert.Equal(HandCategory.FullHouse, v.Category);
        Assert.Equal(9, v.T1);
        Assert.Equal(2, v.T2);
    }

    [Fact]
    public void HighCard_top_five_kickers()
    {
        var hand = Hand(
            (Rank.Ace, Suit.Spades), (Rank.King, Suit.Hearts),
            (Rank.Nine, Suit.Diamonds), (Rank.Seven, Suit.Clubs), (Rank.Five, Suit.Spades),
            (Rank.Three, Suit.Hearts), (Rank.Two, Suit.Diamonds));
        var v = HandEvaluator.EvaluateBest(hand);
        Assert.Equal(HandCategory.HighCard, v.Category);
        Assert.Equal(14, v.T1);
        Assert.Equal(13, v.T2);
        Assert.Equal(9, v.T3);
        Assert.Equal(7, v.T4);
        Assert.Equal(5, v.T5);
    }
}
