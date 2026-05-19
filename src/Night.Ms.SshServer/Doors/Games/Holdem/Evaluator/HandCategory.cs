namespace Night.Ms.SshServer.Doors.Games.Holdem.Evaluator;

// Ordered low-to-high so HandValue comparison can just compare categories first. Royal
// flush is not a distinct category — it's encoded as a straight flush with high card Ace.
public enum HandCategory
{
    HighCard = 0,
    OnePair,
    TwoPair,
    ThreeOfAKind,
    Straight,
    Flush,
    FullHouse,
    FourOfAKind,
    StraightFlush,
}
