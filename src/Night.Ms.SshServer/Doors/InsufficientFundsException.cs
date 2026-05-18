namespace Night.Ms.SshServer.Doors;

// Thrown by IGameLedger.PlayRoundAsync when the requested bet exceeds the user's combined
// (daily + winnings) balance. Games catch this and show a friendly "not enough coins" prompt
// rather than letting it bubble up as a session crash.
public sealed class InsufficientFundsException(int requested, long available)
    : Exception($"Insufficient coins: bet {requested}, available {available}.")
{
    public int Requested { get; } = requested;
    public long Available { get; } = available;
}
