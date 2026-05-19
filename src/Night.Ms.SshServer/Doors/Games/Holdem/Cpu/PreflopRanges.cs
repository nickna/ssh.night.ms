using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

// Chen formula — a well-documented pre-flop hand-strength heuristic. Outputs roughly
// 0 (worst, 7-2 offsuit) to 20 (best, AA). The CPU strategy maps the score to a
// tier and uses the personality to decide play/raise thresholds.
public static class PreflopRanges
{
    public static double ChenScore(Card a, Card b)
    {
        var hiRank = (int)a.Rank >= (int)b.Rank ? a.Rank : b.Rank;
        var loRank = (int)a.Rank >= (int)b.Rank ? b.Rank : a.Rank;

        var score = HighCardScore(hiRank);
        if (a.Rank == b.Rank) score = Math.Max(score * 2, 5);   // pocket pair
        if (a.Suit == b.Suit) score += 2;                         // suited

        var gap = ((int)hiRank - (int)loRank) - 1;
        // For pairs the "gap" is meaningless — pocket pairs already get their bonus above
        // and shouldn't be penalized.
        if (a.Rank != b.Rank)
        {
            score -= gap switch { 0 => 0, 1 => 1, 2 => 2, 3 => 4, _ => 5 };
            // Zero-or-one-gap connector under Queen: small straight bonus.
            if (gap <= 1 && (int)hiRank < (int)Rank.Queen) score += 1;
        }
        // Round half up — Chen specifies fractional values for some cards but engines
        // commonly round.
        return Math.Round(score, MidpointRounding.AwayFromZero);
    }

    private static double HighCardScore(Rank r) => r switch
    {
        Rank.Ace => 10,
        Rank.King => 8,
        Rank.Queen => 7,
        Rank.Jack => 6,
        _ => (int)r / 2.0,
    };
}
