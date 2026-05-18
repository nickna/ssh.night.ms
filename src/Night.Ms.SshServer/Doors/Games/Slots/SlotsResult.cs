namespace Night.Ms.SshServer.Doors.Games.Slots;

// Outcome of one spin: the three reel symbols, the payout multiplier that applied (0 = no
// match), and a short human-readable description for the UI. Engine + screen stay decoupled
// from the paytable shape — adding bonus combinations only touches the engine.
public sealed record SlotsResult(
    SlotSymbol Reel1,
    SlotSymbol Reel2,
    SlotSymbol Reel3,
    int Multiplier,
    string MatchLabel)
{
    public int Payout(int bet) => Multiplier * bet;
}
