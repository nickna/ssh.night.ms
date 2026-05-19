namespace Night.Ms.SshServer.Doors.Games.Holdem;

public enum HoldemActionKind
{
    // Engine sentinel for "the actor missed their timer". Resolves to Check if free,
    // Fold otherwise. Coordinator surfaces it as ApplyTimeout(); the engine never
    // accepts Default through user-driven LegalActions.
    Default,

    Check,
    Call,
    Fold,
    // Bet: opens a betting round (CurrentBet was 0). Amount = target BetThisRound.
    Bet,
    // Raise: increases an existing bet. Amount = target BetThisRound ("raise to" semantics).
    Raise,
    // AllIn: special call/bet/raise pushing the seat's whole stack in. Amount is implicit.
    AllIn,
}
