namespace Night.Ms.SshServer.Doors.Games.Common.Cards;

// Standard 52-card deck, shuffled in-place by Fisher-Yates using the injected IGameRng.
// Draw consumes from the front. One Deck per hand — consumers don't need more than 52
// cards in a single hand for any game we currently ship.
public sealed class Deck
{
    private readonly List<Card> _cards;
    private int _index;

    public Deck(IGameRng rng)
    {
        _cards = new List<Card>(52);
        foreach (var suit in Enum.GetValues<Suit>())
        {
            foreach (var rank in Enum.GetValues<Rank>())
            {
                _cards.Add(new Card(rank, suit));
            }
        }

        for (var i = _cards.Count - 1; i > 0; i--)
        {
            var j = rng.Next(i + 1);
            (_cards[i], _cards[j]) = (_cards[j], _cards[i]);
        }
    }

    // Test seam: build a deck whose Draw order matches the supplied list verbatim. Skips
    // both the standard 52-card build and the Fisher-Yates shuffle so test cases can stack
    // exact card sequences for engine assertions.
    internal Deck(IEnumerable<Card> stackedOrder)
    {
        _cards = stackedOrder.ToList();
    }

    public Card Draw() => _cards[_index++];
    public int Remaining => _cards.Count - _index;
}
