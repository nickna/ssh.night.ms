namespace Night.Ms.SshServer.Doors.Games.Slots;

// Reel weights + payout multipliers. Lives as data (not branches) so future tuning is a
// one-line edit and the engine stays generic. Visible RTP with these numbers is ~73%:
// frequent small wins from the cherry payouts keep the spin loop feeling alive without
// turning the machine into a money pump.
public static class SlotPaytable
{
    // Weighted reel. Probability of landing on a symbol is weight / TotalWeight. All three
    // reels share the same distribution — picking distinct strips per reel is a future
    // tuning lever but not necessary for the v1 player experience.
    public static IReadOnlyDictionary<SlotSymbol, int> ReelWeights { get; } = new Dictionary<SlotSymbol, int>
    {
        [SlotSymbol.Seven]  = 1,
        [SlotSymbol.Bar]    = 2,
        [SlotSymbol.Bell]   = 4,
        [SlotSymbol.Plum]   = 6,
        [SlotSymbol.Lemon]  = 10,
        [SlotSymbol.Cherry] = 15,
        [SlotSymbol.Blank]  = 25,
    };

    public static int TotalWeight => 63; // sum of ReelWeights, kept inline as a compile-time fact

    // Multipliers applied to the user's bet. Mutually exclusive: the engine picks the
    // highest-paying match, never stacks. 1× returns the bet (breakeven); 3× pays net +2×.
    public static IReadOnlyDictionary<SlotSymbol, int> ThreeOfAKindMultipliers { get; } = new Dictionary<SlotSymbol, int>
    {
        [SlotSymbol.Seven]  = 500,
        [SlotSymbol.Bar]    = 200,
        [SlotSymbol.Bell]   = 60,
        [SlotSymbol.Plum]   = 30,
        [SlotSymbol.Lemon]  = 12,
        [SlotSymbol.Cherry] = 8,
    };

    // Cherry has a kicker schedule on top of its 3-of-a-kind line: exactly two cherries
    // anywhere on the row pays 3×, and a single cherry on reel 1 (with neither of the other
    // reels showing cherry) pays 1× (return of bet). Both keep the spin loop feeling alive.
    public const int TwoCherryMultiplier = 3;
    public const int OneCherryReelOneMultiplier = 1;
}
