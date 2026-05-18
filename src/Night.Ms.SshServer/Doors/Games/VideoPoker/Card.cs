namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

public enum Suit { Clubs, Diamonds, Hearts, Spades }

// Numeric Rank values are deliberate: comparisons in the evaluator (Jack or better, straight
// detection, royal-flush top card) treat Rank as the underlying int. Ace is high; the
// ace-low straight (A-2-3-4-5) is handled as a special case in IsStraight.
public enum Rank
{
    Two = 2, Three = 3, Four = 4, Five = 5, Six = 6, Seven = 7,
    Eight = 8, Nine = 9, Ten = 10, Jack = 11, Queen = 12, King = 13, Ace = 14,
}

public sealed record Card(Rank Rank, Suit Suit)
{
    public string RankLabel => Rank switch
    {
        Rank.Ace => "A",
        Rank.King => "K",
        Rank.Queen => "Q",
        Rank.Jack => "J",
        Rank.Ten => "10",
        _ => ((int)Rank).ToString(),
    };

    public string SuitGlyph => Suit switch
    {
        Suit.Clubs => "C",
        Suit.Diamonds => "D",
        Suit.Hearts => "H",
        Suit.Spades => "S",
        _ => "?",
    };

    // ASCII-only on purpose: ♣♦♥♠ have East Asian Width "ambiguous" and render as wide on
    // a handful of SSH clients, which would misalign the row of cards. Letter suits stay
    // exactly one column on every terminal.
    public override string ToString() => RankLabel + SuitGlyph;
}
