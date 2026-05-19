using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Evaluator;

// Picks the best 5-of-7 from 2 hole cards + up to 5 community cards. Algorithmic (not
// table-lookup): builds rank/suit histograms, scans for straight via bitmask, picks
// categories in descending order. ~150 lines; ~5 µs per 7-card eval is fine for our
// Monte Carlo budget.
//
// Accepts 5..7 cards so the same evaluator works for partial boards in the CPU's
// equity simulations (flop = 5 cards: 2 hole + 3 board, etc.).
public static class HandEvaluator
{
    public static HandValue EvaluateBest(IReadOnlyList<Card> cards)
    {
        if (cards.Count is < 5 or > 7)
            throw new ArgumentException($"need 5-7 cards, got {cards.Count}", nameof(cards));

        // rankCount[2..14], indexed by Rank's underlying int.
        Span<int> rankCount = stackalloc int[15];
        // suitMask[0..3] is just the count per suit; suitRankBits[s] is a bitmap over ranks
        // (2..14) for cards of suit s, used to detect a straight flush.
        Span<int> suitCount = stackalloc int[4];
        Span<int> suitRankBits = stackalloc int[4];
        var rankBits = 0;

        foreach (var c in cards)
        {
            var r = (int)c.Rank;
            rankCount[r]++;
            suitCount[(int)c.Suit]++;
            suitRankBits[(int)c.Suit] |= 1 << r;
            rankBits |= 1 << r;
        }

        // Ace-low support for the wheel: mirror Ace into a synthetic "1" bit so the bitmask
        // straight scan finds A-2-3-4-5 the same way it finds any other straight.
        if (rankCount[14] > 0) rankBits |= 1 << 1;

        // Straight flush dominates everything else, so check it first.
        var sfHigh = -1;
        var flushSuit = -1;
        for (var s = 0; s < 4; s++)
        {
            if (suitCount[s] < 5) continue;
            flushSuit = s;
            var bits = suitRankBits[s];
            if ((bits & (1 << 14)) != 0) bits |= 1 << 1;
            sfHigh = ScanStraight(bits);
            if (sfHigh >= 0) break;
        }
        if (sfHigh >= 0) return new HandValue(HandCategory.StraightFlush, sfHigh, 0, 0, 0, 0);

        // Quads / trips / pairs walked from the top so the highest of each kind wins ties.
        int quadRank = -1, kicker;
        var tripsRanks = new List<int>(2);
        var pairRanks = new List<int>(3);
        for (var r = 14; r >= 2; r--)
        {
            switch (rankCount[r])
            {
                case 4: quadRank = r; break;
                case 3: tripsRanks.Add(r); break;
                case 2: pairRanks.Add(r); break;
            }
        }

        if (quadRank >= 0)
        {
            kicker = HighestExcluding(rankCount, quadRank, -1, -1, -1);
            return new HandValue(HandCategory.FourOfAKind, quadRank, kicker, 0, 0, 0);
        }

        // Full house: a trips plus either a pair OR another trips (downgraded to pair).
        if (tripsRanks.Count >= 1 && (pairRanks.Count >= 1 || tripsRanks.Count >= 2))
        {
            var trips = tripsRanks[0];
            var pair = pairRanks.Count > 0
                ? Math.Max(pairRanks[0], tripsRanks.Count >= 2 ? tripsRanks[1] : 0)
                : tripsRanks[1];
            return new HandValue(HandCategory.FullHouse, trips, pair, 0, 0, 0);
        }

        if (flushSuit >= 0)
        {
            var bits = suitRankBits[flushSuit];
            Span<int> top = stackalloc int[5];
            var count = 0;
            for (var r = 14; r >= 2 && count < 5; r--)
            {
                if ((bits & (1 << r)) != 0) top[count++] = r;
            }
            return new HandValue(HandCategory.Flush, top[0], top[1], top[2], top[3], top[4]);
        }

        var straightHigh = ScanStraight(rankBits);
        if (straightHigh >= 0)
            return new HandValue(HandCategory.Straight, straightHigh, 0, 0, 0, 0);

        if (tripsRanks.Count >= 1)
        {
            var trips = tripsRanks[0];
            var k1 = HighestExcluding(rankCount, trips, -1, -1, -1);
            var k2 = HighestExcluding(rankCount, trips, k1, -1, -1);
            return new HandValue(HandCategory.ThreeOfAKind, trips, k1, k2, 0, 0);
        }

        if (pairRanks.Count >= 2)
        {
            var hi = pairRanks[0];
            var lo = pairRanks[1];
            var k = HighestExcluding(rankCount, hi, lo, -1, -1);
            return new HandValue(HandCategory.TwoPair, hi, lo, k, 0, 0);
        }

        if (pairRanks.Count == 1)
        {
            var pair = pairRanks[0];
            var k1 = HighestExcluding(rankCount, pair, -1, -1, -1);
            var k2 = HighestExcluding(rankCount, pair, k1, -1, -1);
            var k3 = HighestExcluding(rankCount, pair, k1, k2, -1);
            return new HandValue(HandCategory.OnePair, pair, k1, k2, k3, 0);
        }

        // High card: top 5 ranks descending.
        Span<int> hi5 = stackalloc int[5];
        var n = 0;
        for (var r = 14; r >= 2 && n < 5; r--)
        {
            if (rankCount[r] > 0) hi5[n++] = r;
        }
        return new HandValue(HandCategory.HighCard, hi5[0], hi5[1], hi5[2], hi5[3], hi5[4]);
    }

    // Returns the high rank of a straight present in the bitmask (with bit 1 set for an
    // ace-low wheel). -1 if no straight. Scans 14 → 5 so highest straight wins.
    private static int ScanStraight(int bits)
    {
        for (var high = 14; high >= 5; high--)
        {
            var mask = (1 << high) | (1 << (high - 1)) | (1 << (high - 2)) | (1 << (high - 3)) | (1 << (high - 4));
            if ((bits & mask) == mask) return high;
        }
        return -1;
    }

    // Highest rank whose count > 0 and that isn't in the excluded set. Used to pick
    // kickers without allocating. Pass -1 for unused exclusions.
    private static int HighestExcluding(ReadOnlySpan<int> rankCount, int e1, int e2, int e3, int e4)
    {
        for (var r = 14; r >= 2; r--)
        {
            if (rankCount[r] == 0) continue;
            if (r == e1 || r == e2 || r == e3 || r == e4) continue;
            return r;
        }
        return 0;
    }
}
