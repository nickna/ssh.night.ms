namespace Night.Ms.SshServer.Doors.Games.Holdem;

public static class HoldemRules
{
    public const int MaxSeats = 6;
    public const int MinSeatedToStart = 2;

    // 30s per action; 3 consecutive timeouts flips the seat to SittingOut. Tuned to match
    // a casual BBS pace — slow enough that a thoughtful raise is fine, fast enough that
    // tables don't stall behind one AFK player.
    public const int TurnSeconds = 30;
    public const int MissesBeforeSitOut = 3;

    // Single 6-max table v1, blinds 5/10. Future tables can pass distinct values to
    // HoldemTableState's constructor; rules above are global.
    public const long DefaultSmallBlind = 5;
    public const long DefaultBigBlind = 10;
    public const int DefaultMinBuyInChips = 100;   // 10× BB
    public const int DefaultMaxBuyInChips = 2000;  // 200× BB

    // CPU "think time" between deciding and acting. Random within [Min, Max], biased per
    // action class (fold = fast, raise = slow) inside the coordinator. Settable (not const)
    // so integration tests can zero them out for fast determinism; flipping these to Zero
    // disables the delay entirely.
    public static TimeSpan CpuThinkMin = TimeSpan.FromMilliseconds(600);
    public static TimeSpan CpuThinkMax = TimeSpan.FromMilliseconds(1800);
}
