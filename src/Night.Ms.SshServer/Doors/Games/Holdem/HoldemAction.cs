namespace Night.Ms.SshServer.Doors.Games.Holdem;

// One action submitted to the engine. Amount semantics depend on Kind:
//   - Check/Call/Fold/AllIn/Default: Amount is ignored.
//   - Bet: Amount is the target BetThisRound. Must equal Stack delta you intend to spend
//     (since BetThisRound was 0 at the start of the betting round).
//   - Raise: Amount is the "raise to" total — the new BetThisRound the actor is reaching.
//     Chips spent = Amount - seat.BetThisRound.
public readonly record struct HoldemAction(HoldemActionKind Kind, long Amount)
{
    public static HoldemAction Check() => new(HoldemActionKind.Check, 0);
    public static HoldemAction Call() => new(HoldemActionKind.Call, 0);
    public static HoldemAction Fold() => new(HoldemActionKind.Fold, 0);
    public static HoldemAction Bet(long toAmount) => new(HoldemActionKind.Bet, toAmount);
    public static HoldemAction Raise(long toAmount) => new(HoldemActionKind.Raise, toAmount);
    public static HoldemAction AllIn() => new(HoldemActionKind.AllIn, 0);
    public static HoldemAction Default() => new(HoldemActionKind.Default, 0);
}
