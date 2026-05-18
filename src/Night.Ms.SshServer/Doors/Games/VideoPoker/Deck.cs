namespace Night.Ms.SshServer.Doors.Games.VideoPoker;

// Standard 52-card deck, shuffled in-place by Fisher-Yates using the injected IGameRng.
// Draw consumes from the front; the deck doesn't recycle within a hand because we only
// ever need up to 10 cards (5 dealt + 5 redraws).
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

    public Card Draw() => _cards[_index++];
    public int Remaining => _cards.Count - _index;
}
