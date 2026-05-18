namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

// Ordered weakest → strongest so callers can compare via the underlying int. Names match the
// canonical 9/6 Jacks-or-Better paytable rows.
public enum HandRank
{
    HighCard,
    JacksOrBetter,
    TwoPair,
    ThreeOfAKind,
    Straight,
    Flush,
    FullHouse,
    FourOfAKind,
    StraightFlush,
    RoyalFlush,
}

public static class HandRankExtensions
{
    public static string DisplayName(this HandRank rank) => rank switch
    {
        HandRank.RoyalFlush => "Royal Flush",
        HandRank.StraightFlush => "Straight Flush",
        HandRank.FourOfAKind => "Four of a Kind",
        HandRank.FullHouse => "Full House",
        HandRank.Flush => "Flush",
        HandRank.Straight => "Straight",
        HandRank.ThreeOfAKind => "Three of a Kind",
        HandRank.TwoPair => "Two Pair",
        HandRank.JacksOrBetter => "Jacks or Better",
        HandRank.HighCard => "Nothing",
        _ => rank.ToString(),
    };
}
