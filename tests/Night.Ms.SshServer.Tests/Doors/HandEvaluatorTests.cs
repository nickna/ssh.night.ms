using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.VideoPoker;

namespace Night.Ms.SshServer.Tests.Doors;

public class HandEvaluatorTests
{
    private static Card C(Rank r, Suit s) => new(r, s);

    [Fact]
    public void RoyalFlush_T_J_Q_K_A_same_suit()
    {
        var hand = new[]
        {
            C(Rank.Ten, Suit.Spades), C(Rank.Jack, Suit.Spades), C(Rank.Queen, Suit.Spades),
            C(Rank.King, Suit.Spades), C(Rank.Ace, Suit.Spades),
        };
        Assert.Equal(HandRank.RoyalFlush, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void StraightFlush_9_T_J_Q_K_same_suit()
    {
        var hand = new[]
        {
            C(Rank.Nine, Suit.Hearts), C(Rank.Ten, Suit.Hearts), C(Rank.Jack, Suit.Hearts),
            C(Rank.Queen, Suit.Hearts), C(Rank.King, Suit.Hearts),
        };
        Assert.Equal(HandRank.StraightFlush, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void StraightFlush_ace_low_steel_wheel()
    {
        // A-2-3-4-5 same suit is a straight flush, but NOT a royal flush.
        var hand = new[]
        {
            C(Rank.Ace, Suit.Diamonds), C(Rank.Two, Suit.Diamonds), C(Rank.Three, Suit.Diamonds),
            C(Rank.Four, Suit.Diamonds), C(Rank.Five, Suit.Diamonds),
        };
        Assert.Equal(HandRank.StraightFlush, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void FourOfAKind()
    {
        var hand = new[]
        {
            C(Rank.Eight, Suit.Clubs), C(Rank.Eight, Suit.Diamonds), C(Rank.Eight, Suit.Hearts),
            C(Rank.Eight, Suit.Spades), C(Rank.Two, Suit.Clubs),
        };
        Assert.Equal(HandRank.FourOfAKind, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void FullHouse()
    {
        var hand = new[]
        {
            C(Rank.King, Suit.Clubs), C(Rank.King, Suit.Diamonds), C(Rank.King, Suit.Hearts),
            C(Rank.Three, Suit.Spades), C(Rank.Three, Suit.Clubs),
        };
        Assert.Equal(HandRank.FullHouse, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void Flush_unconnected_same_suit()
    {
        var hand = new[]
        {
            C(Rank.Two, Suit.Spades), C(Rank.Five, Suit.Spades), C(Rank.Eight, Suit.Spades),
            C(Rank.Jack, Suit.Spades), C(Rank.King, Suit.Spades),
        };
        Assert.Equal(HandRank.Flush, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void Straight_mixed_suits()
    {
        var hand = new[]
        {
            C(Rank.Four, Suit.Clubs), C(Rank.Five, Suit.Diamonds), C(Rank.Six, Suit.Hearts),
            C(Rank.Seven, Suit.Spades), C(Rank.Eight, Suit.Clubs),
        };
        Assert.Equal(HandRank.Straight, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void Straight_ace_low_wheel()
    {
        var hand = new[]
        {
            C(Rank.Ace, Suit.Clubs), C(Rank.Two, Suit.Diamonds), C(Rank.Three, Suit.Hearts),
            C(Rank.Four, Suit.Spades), C(Rank.Five, Suit.Clubs),
        };
        Assert.Equal(HandRank.Straight, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void Straight_ace_high_broadway_not_royal_when_mixed()
    {
        var hand = new[]
        {
            C(Rank.Ten, Suit.Clubs), C(Rank.Jack, Suit.Diamonds), C(Rank.Queen, Suit.Hearts),
            C(Rank.King, Suit.Spades), C(Rank.Ace, Suit.Clubs),
        };
        Assert.Equal(HandRank.Straight, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void Q_K_A_2_3_is_not_a_straight()
    {
        // Wrap-around straights are NOT legal in this game.
        var hand = new[]
        {
            C(Rank.Queen, Suit.Clubs), C(Rank.King, Suit.Diamonds), C(Rank.Ace, Suit.Hearts),
            C(Rank.Two, Suit.Spades), C(Rank.Three, Suit.Clubs),
        };
        Assert.NotEqual(HandRank.Straight, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void ThreeOfAKind()
    {
        var hand = new[]
        {
            C(Rank.Nine, Suit.Clubs), C(Rank.Nine, Suit.Diamonds), C(Rank.Nine, Suit.Hearts),
            C(Rank.Four, Suit.Spades), C(Rank.Two, Suit.Clubs),
        };
        Assert.Equal(HandRank.ThreeOfAKind, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void TwoPair()
    {
        var hand = new[]
        {
            C(Rank.Ten, Suit.Clubs), C(Rank.Ten, Suit.Diamonds), C(Rank.Three, Suit.Hearts),
            C(Rank.Three, Suit.Spades), C(Rank.King, Suit.Clubs),
        };
        Assert.Equal(HandRank.TwoPair, HandEvaluator.Evaluate(hand));
    }

    [Theory]
    [InlineData(Rank.Jack)]
    [InlineData(Rank.Queen)]
    [InlineData(Rank.King)]
    [InlineData(Rank.Ace)]
    public void JacksOrBetter_pair_pays(Rank pairRank)
    {
        var hand = new[]
        {
            C(pairRank, Suit.Clubs), C(pairRank, Suit.Diamonds), C(Rank.Three, Suit.Hearts),
            C(Rank.Six, Suit.Spades), C(Rank.Eight, Suit.Clubs),
        };
        Assert.Equal(HandRank.JacksOrBetter, HandEvaluator.Evaluate(hand));
    }

    [Theory]
    [InlineData(Rank.Two)]
    [InlineData(Rank.Ten)]
    public void PairBelowJacks_is_HighCard(Rank pairRank)
    {
        var hand = new[]
        {
            C(pairRank, Suit.Clubs), C(pairRank, Suit.Diamonds), C(Rank.Three, Suit.Hearts),
            C(Rank.Six, Suit.Spades), C(Rank.Eight, Suit.Clubs),
        };
        Assert.Equal(HandRank.HighCard, HandEvaluator.Evaluate(hand));
    }

    [Fact]
    public void HighCard_when_nothing_matches()
    {
        var hand = new[]
        {
            C(Rank.Two, Suit.Clubs), C(Rank.Five, Suit.Diamonds), C(Rank.Eight, Suit.Hearts),
            C(Rank.Jack, Suit.Spades), C(Rank.King, Suit.Clubs),
        };
        Assert.Equal(HandRank.HighCard, HandEvaluator.Evaluate(hand));
    }

    [Theory]
    [InlineData(HandRank.RoyalFlush, 25, 4000)]   // max coin bonus
    [InlineData(HandRank.RoyalFlush, 20, 1000)]   // 4 coins × 250
    [InlineData(HandRank.RoyalFlush, 5, 250)]
    [InlineData(HandRank.StraightFlush, 25, 250)] // 5 × 50
    [InlineData(HandRank.FullHouse, 25, 45)]      // 5 × 9
    [InlineData(HandRank.Flush, 25, 30)]          // 5 × 6
    [InlineData(HandRank.JacksOrBetter, 5, 1)]
    [InlineData(HandRank.HighCard, 25, 0)]
    public void Paytable_scales_per_coin_with_max_coin_royal_bonus(HandRank rank, int bet, int expected)
    {
        Assert.Equal(expected, VideoPokerPaytable.Payout(bet, rank));
    }
}
