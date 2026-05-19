using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.VideoPoker;

namespace Night.Ms.SshServer.Tests.Doors;

public class WinningCardFinderTests
{
    private static Card C(Rank r, Suit s) => new(r, s);

    [Fact]
    public void RoyalFlush_returns_all_five()
    {
        var hand = new[]
        {
            C(Rank.Ten, Suit.Spades), C(Rank.Jack, Suit.Spades), C(Rank.Queen, Suit.Spades),
            C(Rank.King, Suit.Spades), C(Rank.Ace, Suit.Spades),
        };
        Assert.Equal(new[] { 0, 1, 2, 3, 4 }, WinningCardFinder.Find(hand, HandRank.RoyalFlush));
    }

    [Fact]
    public void StraightFlush_returns_all_five()
    {
        var hand = new[]
        {
            C(Rank.Nine, Suit.Hearts), C(Rank.Ten, Suit.Hearts), C(Rank.Jack, Suit.Hearts),
            C(Rank.Queen, Suit.Hearts), C(Rank.King, Suit.Hearts),
        };
        Assert.Equal(new[] { 0, 1, 2, 3, 4 }, WinningCardFinder.Find(hand, HandRank.StraightFlush));
    }

    [Fact]
    public void FullHouse_returns_all_five()
    {
        var hand = new[]
        {
            C(Rank.Eight, Suit.Clubs), C(Rank.Eight, Suit.Diamonds), C(Rank.Eight, Suit.Hearts),
            C(Rank.Two, Suit.Clubs), C(Rank.Two, Suit.Spades),
        };
        Assert.Equal(new[] { 0, 1, 2, 3, 4 }, WinningCardFinder.Find(hand, HandRank.FullHouse));
    }

    [Fact]
    public void Flush_returns_all_five()
    {
        var hand = new[]
        {
            C(Rank.Two, Suit.Hearts), C(Rank.Five, Suit.Hearts), C(Rank.Nine, Suit.Hearts),
            C(Rank.Jack, Suit.Hearts), C(Rank.King, Suit.Hearts),
        };
        Assert.Equal(new[] { 0, 1, 2, 3, 4 }, WinningCardFinder.Find(hand, HandRank.Flush));
    }

    [Fact]
    public void Straight_returns_all_five()
    {
        var hand = new[]
        {
            C(Rank.Five, Suit.Hearts), C(Rank.Six, Suit.Clubs), C(Rank.Seven, Suit.Diamonds),
            C(Rank.Eight, Suit.Spades), C(Rank.Nine, Suit.Hearts),
        };
        Assert.Equal(new[] { 0, 1, 2, 3, 4 }, WinningCardFinder.Find(hand, HandRank.Straight));
    }

    [Fact]
    public void FourOfAKind_returns_indices_of_four_matching_cards()
    {
        // Kicker at index 4. The four eights are at indices 0, 1, 2, 3.
        var hand = new[]
        {
            C(Rank.Eight, Suit.Clubs), C(Rank.Eight, Suit.Diamonds), C(Rank.Eight, Suit.Hearts),
            C(Rank.Eight, Suit.Spades), C(Rank.Two, Suit.Clubs),
        };
        Assert.Equal(new[] { 0, 1, 2, 3 }, WinningCardFinder.Find(hand, HandRank.FourOfAKind));
    }

    [Fact]
    public void FourOfAKind_with_kicker_first()
    {
        // Kicker at index 0; the four nines occupy indices 1-4.
        var hand = new[]
        {
            C(Rank.Two, Suit.Clubs), C(Rank.Nine, Suit.Clubs), C(Rank.Nine, Suit.Diamonds),
            C(Rank.Nine, Suit.Hearts), C(Rank.Nine, Suit.Spades),
        };
        Assert.Equal(new[] { 1, 2, 3, 4 }, WinningCardFinder.Find(hand, HandRank.FourOfAKind));
    }

    [Fact]
    public void ThreeOfAKind_returns_indices_of_three_matching_cards()
    {
        // Three jacks at indices 0, 2, 4 (interleaved with kickers).
        var hand = new[]
        {
            C(Rank.Jack, Suit.Clubs), C(Rank.Five, Suit.Diamonds), C(Rank.Jack, Suit.Hearts),
            C(Rank.Two, Suit.Spades), C(Rank.Jack, Suit.Spades),
        };
        Assert.Equal(new[] { 0, 2, 4 }, WinningCardFinder.Find(hand, HandRank.ThreeOfAKind));
    }

    [Fact]
    public void TwoPair_returns_all_four_paired_indices()
    {
        // Kings at 0, 3; threes at 1, 4; kicker at 2.
        var hand = new[]
        {
            C(Rank.King, Suit.Clubs), C(Rank.Three, Suit.Diamonds), C(Rank.Seven, Suit.Hearts),
            C(Rank.King, Suit.Spades), C(Rank.Three, Suit.Spades),
        };
        Assert.Equal(new[] { 0, 1, 3, 4 }, WinningCardFinder.Find(hand, HandRank.TwoPair));
    }

    [Fact]
    public void JacksOrBetter_returns_indices_of_qualifying_pair()
    {
        // Pair of queens at indices 1 and 3; three garbage kickers.
        var hand = new[]
        {
            C(Rank.Two, Suit.Clubs), C(Rank.Queen, Suit.Diamonds), C(Rank.Five, Suit.Hearts),
            C(Rank.Queen, Suit.Spades), C(Rank.Seven, Suit.Clubs),
        };
        Assert.Equal(new[] { 1, 3 }, WinningCardFinder.Find(hand, HandRank.JacksOrBetter));
    }

    [Fact]
    public void HighCard_returns_empty()
    {
        var hand = new[]
        {
            C(Rank.Two, Suit.Clubs), C(Rank.Five, Suit.Diamonds), C(Rank.Seven, Suit.Hearts),
            C(Rank.Nine, Suit.Spades), C(Rank.Jack, Suit.Clubs),
        };
        Assert.Empty(WinningCardFinder.Find(hand, HandRank.HighCard));
    }
}
