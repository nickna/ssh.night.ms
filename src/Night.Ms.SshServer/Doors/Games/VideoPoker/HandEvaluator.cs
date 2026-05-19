using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

// Classifies a 5-card hand into one of the HandRank tiers used by the 9/6 Jacks-or-Better
// paytable. Pure — no RNG, no I/O — so tests can feed arbitrary hands directly.
public static class HandEvaluator
{
    public static HandRank Evaluate(IReadOnlyList<Card> hand)
    {
        if (hand.Count != 5)
            throw new ArgumentException($"Expected 5 cards, got {hand.Count}.", nameof(hand));

        var ranks = hand.Select(c => c.Rank).OrderBy(r => (int)r).ToArray();
        var isFlush = hand.All(c => c.Suit == hand[0].Suit);
        var isStraight = IsStraight(ranks);

        if (isStraight && isFlush)
        {
            // Royal needs to be Ten-through-Ace specifically; an ace-low straight (5-high)
            // does NOT qualify even though it ends with an Ace in sorted order.
            return ranks[0] == Rank.Ten && ranks[4] == Rank.Ace
                ? HandRank.RoyalFlush
                : HandRank.StraightFlush;
        }

        // Group by rank. Sizes sorted descending give a fingerprint:
        //   [4,1]      → four of a kind
        //   [3,2]      → full house
        //   [3,1,1]    → three of a kind
        //   [2,2,1]    → two pair
        //   [2,1,1,1]  → one pair (jacks-or-better gate decides which way it pays)
        //   [1,1,1,1,1]→ no kind (already handled flush/straight above; otherwise high card)
        var groups = ranks.GroupBy(r => r).ToArray();
        var sizes = groups.Select(g => g.Count()).OrderByDescending(c => c).ToArray();

        if (sizes[0] == 4) return HandRank.FourOfAKind;
        if (sizes[0] == 3 && sizes[1] == 2) return HandRank.FullHouse;
        if (isFlush) return HandRank.Flush;
        if (isStraight) return HandRank.Straight;
        if (sizes[0] == 3) return HandRank.ThreeOfAKind;
        if (sizes[0] == 2 && sizes[1] == 2) return HandRank.TwoPair;
        if (sizes[0] == 2)
        {
            var pairRank = groups.First(g => g.Count() == 2).Key;
            return pairRank >= Rank.Jack ? HandRank.JacksOrBetter : HandRank.HighCard;
        }
        return HandRank.HighCard;
    }

    private static bool IsStraight(Rank[] sortedRanks)
    {
        // Ace-low straight: 2-3-4-5-A. The OrderBy on Rank put Ace last (value 14), so we
        // check the literal sequence rather than running the normal consecutive-step test.
        if (sortedRanks[0] == Rank.Two &&
            sortedRanks[1] == Rank.Three &&
            sortedRanks[2] == Rank.Four &&
            sortedRanks[3] == Rank.Five &&
            sortedRanks[4] == Rank.Ace)
        {
            return true;
        }

        for (var i = 1; i < sortedRanks.Length; i++)
        {
            if ((int)sortedRanks[i] != (int)sortedRanks[i - 1] + 1) return false;
        }
        return true;
    }
}
