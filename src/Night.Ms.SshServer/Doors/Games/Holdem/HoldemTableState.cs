using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Mutable per-hand state. The engine reads + mutates this directly. The coordinator owns
// snapshotting/persisting it (currently to Redis). One instance per table; replaced with
// a fresh Deck at the start of each hand (RebindDeck).
public sealed class HoldemTableState
{
    public HoldemTableState(int seatCount, long smallBlind, long bigBlind, IGameRng rng)
    {
        ArgumentOutOfRangeException.ThrowIfLessThan(seatCount, 2);
        ArgumentOutOfRangeException.ThrowIfNegativeOrZero(smallBlind);
        ArgumentOutOfRangeException.ThrowIfLessThan(bigBlind, smallBlind);

        Seats = new List<HoldemSeatState>(seatCount);
        for (var i = 0; i < seatCount; i++) Seats.Add(new HoldemSeatState());
        SmallBlind = smallBlind;
        BigBlind = bigBlind;
        Rng = rng;
        Deck = new Deck(rng);
    }

    public List<HoldemSeatState> Seats { get; }
    public Deck Deck { get; private set; }
    public IGameRng Rng { get; }
    public long SmallBlind { get; }
    public long BigBlind { get; }

    public int DealerButton { get; set; }
    public HoldemPhase Phase { get; set; } = HoldemPhase.Idle;
    public int? ActorIndex { get; set; }
    public List<Card> Board { get; } = new(5);
    public long CurrentBet { get; set; }
    public long MinRaise { get; set; }
    public int HandNumber { get; set; }

    public HoldemPotStructure? Pots { get; set; }
    public List<HoldemSeatPayout> Payouts { get; } = new();

    // Per-seat miss counter for the 3-strikes sit-out rule. Resets to 0 on any non-default
    // user-driven action.
    public Dictionary<int, int> ConsecutiveMisses { get; } = new();

    // Replace the deck for a new hand. Tests can pass a pre-stacked deck; production uses
    // a fresh Deck(Rng).
    public void RebindDeck(Deck deck) => Deck = deck;
}
