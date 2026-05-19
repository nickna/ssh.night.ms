using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

// Given a final hand and the HandRank that HandEvaluator returned for it, surface the
// 0-based indices of the cards that form the paying combination. Used by the table view
// to outline winning cards in gold. Lives in screen-layer code (not HandEvaluator) so
// the engine and its existing tests stay byte-identical.
//
// All paying ranks return at least one index; HighCard returns empty.
internal static class WinningCardFinder
{
    public static int[] Find(Card[] hand, HandRank rank) => rank switch
    {
        HandRank.RoyalFlush => new[] { 0, 1, 2, 3, 4 },
        HandRank.StraightFlush => new[] { 0, 1, 2, 3, 4 },
        HandRank.FullHouse => new[] { 0, 1, 2, 3, 4 },
        HandRank.Flush => new[] { 0, 1, 2, 3, 4 },
        HandRank.Straight => new[] { 0, 1, 2, 3, 4 },
        HandRank.FourOfAKind => IndicesOfGroup(hand, 4),
        HandRank.ThreeOfAKind => IndicesOfGroup(hand, 3),
        HandRank.TwoPair => IndicesOfTwoPair(hand),
        HandRank.JacksOrBetter => IndicesOfQualifyingPair(hand),
        _ => Array.Empty<int>(),
    };

    private static int[] IndicesOfGroup(Card[] hand, int targetCount)
    {
        var match = hand
            .Select((c, i) => (c.Rank, Index: i))
            .GroupBy(x => x.Rank)
            .FirstOrDefault(g => g.Count() == targetCount);
        return match is null
            ? Array.Empty<int>()
            : match.Select(x => x.Index).OrderBy(i => i).ToArray();
    }

    private static int[] IndicesOfTwoPair(Card[] hand)
    {
        var pairs = hand
            .Select((c, i) => (c.Rank, Index: i))
            .GroupBy(x => x.Rank)
            .Where(g => g.Count() == 2);
        return pairs
            .SelectMany(g => g.Select(x => x.Index))
            .OrderBy(i => i)
            .ToArray();
    }

    private static int[] IndicesOfQualifyingPair(Card[] hand)
    {
        // JacksOrBetter is reached only when exactly one pair exists AND its rank ≥ Jack.
        // Find that pair; if no qualifying pair, fall back to empty (defensive — caller is
        // expected to invoke this only when rank == JacksOrBetter).
        var pair = hand
            .Select((c, i) => (c.Rank, Index: i))
            .GroupBy(x => x.Rank)
            .Where(g => g.Count() == 2 && (int)g.Key >= (int)Rank.Jack)
            .FirstOrDefault();
        return pair is null
            ? Array.Empty<int>()
            : pair.Select(x => x.Index).OrderBy(i => i).ToArray();
    }
}
