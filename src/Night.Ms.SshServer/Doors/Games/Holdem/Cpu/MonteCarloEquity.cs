using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem.Evaluator;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

// Estimates hero's win probability against `opponents` random hands on the current board.
// Returns (wins + ties × 0.5) / sims so it cleanly maps to "equity required to call."
//
// Performance budget: ~5 µs per 7-card eval × (opponents+1) evals × sims ≈ 25 ms at
// sims=1000, opp=5. The CPU has a 30s action timer, so plenty of headroom.
public static class MonteCarloEquity
{
    public static double Estimate(
        IReadOnlyList<Card> heroHole,
        IReadOnlyList<Card> board,
        int opponents,
        int simulations,
        IGameRng rng)
    {
        ArgumentOutOfRangeException.ThrowIfLessThan(opponents, 1);
        ArgumentOutOfRangeException.ThrowIfLessThanOrEqual(simulations, 0);
        if (heroHole.Count != 2) throw new ArgumentException("hero needs exactly 2 hole cards");
        if (board.Count > 5) throw new ArgumentException("board cannot exceed 5 cards");

        var remaining = BuildRemainingDeck(heroHole, board);
        var cardsToDeal = opponents * 2 + (5 - board.Count);
        if (cardsToDeal > remaining.Count)
            throw new ArgumentException("not enough cards left to simulate");

        int wins = 0, ties = 0;

        // Reusable buffers; cleared per sim by overwriting.
        var sevenBuf = new Card[7];
        var fullBoard = new Card[5];
        for (var i = 0; i < board.Count; i++) fullBoard[i] = board[i];

        for (var sim = 0; sim < simulations; sim++)
        {
            // Partial Fisher-Yates: only need to randomize the first cardsToDeal slots,
            // since we never look at the rest.
            for (var k = 0; k < cardsToDeal; k++)
            {
                var j = k + rng.Next(remaining.Count - k);
                (remaining[k], remaining[j]) = (remaining[j], remaining[k]);
            }

            // Complete board from the head of the shuffled buffer.
            var cursor = 0;
            for (var b = board.Count; b < 5; b++) fullBoard[b] = remaining[cursor++];

            // Hero's best 5-of-7.
            sevenBuf[0] = heroHole[0];
            sevenBuf[1] = heroHole[1];
            for (var i = 0; i < 5; i++) sevenBuf[2 + i] = fullBoard[i];
            var heroValue = HandEvaluator.EvaluateBest(sevenBuf);

            // Walk opponents.
            var beaten = false;
            var tied = false;
            for (var o = 0; o < opponents; o++)
            {
                sevenBuf[0] = remaining[cursor++];
                sevenBuf[1] = remaining[cursor++];
                for (var i = 0; i < 5; i++) sevenBuf[2 + i] = fullBoard[i];
                var opp = HandEvaluator.EvaluateBest(sevenBuf);
                var cmp = heroValue.CompareTo(opp);
                if (cmp < 0) { beaten = true; break; }
                if (cmp == 0) tied = true;
            }
            if (!beaten)
            {
                if (tied) ties++;
                else wins++;
            }
        }
        return (wins + ties * 0.5) / simulations;
    }

    private static List<Card> BuildRemainingDeck(IReadOnlyList<Card> hole, IReadOnlyList<Card> board)
    {
        var taken = new HashSet<Card>(hole);
        foreach (var c in board) taken.Add(c);
        var deck = new List<Card>(52 - taken.Count);
        foreach (var suit in Enum.GetValues<Suit>())
        {
            foreach (var rank in Enum.GetValues<Rank>())
            {
                var card = new Card(rank, suit);
                if (!taken.Contains(card)) deck.Add(card);
            }
        }
        return deck;
    }
}
