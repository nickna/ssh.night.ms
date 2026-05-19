namespace Night.Ms.SshServer.Doors.Games.Common.Cards;

public enum Suit { Clubs, Diamonds, Hearts, Spades }

// Numeric Rank values are deliberate: card games compare Rank as the underlying int.
// Ace is high; ace-low straights (A-2-3-4-5) and blackjack's soft-ace logic are handled
// by the consuming engine, not here.
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

    // Unicode pip. ♠♥♦♣ are East Asian Width "ambiguous" and a few clients render them as
    // emoji (double-wide), which would misalign rows of cards. CardSprites pairs each pip
    // cell with U+FE0E (text-presentation variation selector) via Cell.Modifier to force
    // single-cell rendering on clients that respect it.
    public char SuitGlyph => Suit switch
    {
        Suit.Clubs    => '♣', // ♣
        Suit.Diamonds => '♦', // ♦
        Suit.Hearts   => '♥', // ♥
        Suit.Spades   => '♠', // ♠
        _ => '?',
    };

    public override string ToString() => RankLabel + SuitGlyph;
}
