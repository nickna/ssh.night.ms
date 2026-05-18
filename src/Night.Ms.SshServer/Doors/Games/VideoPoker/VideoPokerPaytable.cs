namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

// Classic 9/6 Jacks-or-Better paytable. The "9/6" means the Full House row pays 9× per coin
// and the Flush row pays 6× per coin — those two numbers define the dialect; everything
// else is standard. Total RTP at optimal play is ~99.5%, so the game favors the player at
// max-coin play. Tunable but the 9/6 numbers are well-studied and feel honest.
//
// Bet in our system is a multiple of 5; "coin level" is bet/5 ∈ [1, 5]. Most rows scale
// linearly with coin level — except Royal Flush at max coin, which jumps from 1250 (5×250)
// to 4000 to mirror the classic max-coin bonus. That bonus is what makes max-coin betting
// the correct strategy long-term.
public static class VideoPokerPaytable
{
    public const int CoinSize = 5;
    public const int MaxCoinLevel = 5;

    public static int PerCoinPayout(HandRank rank) => rank switch
    {
        HandRank.RoyalFlush => 250,
        HandRank.StraightFlush => 50,
        HandRank.FourOfAKind => 25,
        HandRank.FullHouse => 9,
        HandRank.Flush => 6,
        HandRank.Straight => 4,
        HandRank.ThreeOfAKind => 3,
        HandRank.TwoPair => 2,
        HandRank.JacksOrBetter => 1,
        _ => 0,
    };

    public const int MaxCoinRoyalFlushPayout = 4000;

    public static int Payout(int bet, HandRank rank)
    {
        if (bet <= 0 || rank == HandRank.HighCard) return 0;
        var coinLevel = bet / CoinSize;
        if (coinLevel <= 0) return 0;
        if (rank == HandRank.RoyalFlush && coinLevel >= MaxCoinLevel)
            return MaxCoinRoyalFlushPayout;
        return PerCoinPayout(rank) * coinLevel;
    }
}
