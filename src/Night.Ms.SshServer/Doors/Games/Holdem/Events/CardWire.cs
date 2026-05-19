using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Events;

// Compact wire encoding for cards: "As" / "Td" / "Kh" / "2c". Lowercase suit letter so the
// glyph stays one column and we don't accidentally collide with the bus framing or the
// chat protocol's uppercase tags.
public static class CardWire
{
    public static string Encode(Card c) =>
        c.RankLabel + c.Suit switch
        {
            Suit.Clubs => "c",
            Suit.Diamonds => "d",
            Suit.Hearts => "h",
            Suit.Spades => "s",
            _ => "?",
        };

    public static Card Parse(string s)
    {
        if (string.IsNullOrEmpty(s) || s.Length is < 2 or > 3)
            throw new ArgumentException($"invalid card '{s}'");
        // Rank is everything except the last char ("10x" stays 2 chars for rank).
        var rankPart = s[..^1];
        var rank = rankPart switch
        {
            "A" => Rank.Ace,
            "K" => Rank.King,
            "Q" => Rank.Queen,
            "J" => Rank.Jack,
            "10" => Rank.Ten,
            _ => (Rank)int.Parse(rankPart),
        };
        var suit = s[^1] switch
        {
            'c' or 'C' => Suit.Clubs,
            'd' or 'D' => Suit.Diamonds,
            'h' or 'H' => Suit.Hearts,
            's' or 'S' => Suit.Spades,
            _ => throw new ArgumentException($"invalid suit '{s[^1]}'"),
        };
        return new Card(rank, suit);
    }
}
