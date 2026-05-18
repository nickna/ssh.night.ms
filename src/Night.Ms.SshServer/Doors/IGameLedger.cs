using System.Text.Json;

namespace Night.Ms.SshServer.Doors;

public interface IGameLedger
{
    // Atomically applies a round. Inside one Postgres transaction:
    //   1. Refreshes the daily bucket if stale (UTC date roll-over).
    //   2. Debits the bet — daily-first, then falling through to winnings.
    //   3. Credits the payout to winnings.
    //   4. Inserts a game_rounds audit row.
    // The wallet row is locked with SELECT … FOR UPDATE for the duration, so concurrent
    // SSH sessions for the same user can't double-spend.
    // Throws InsufficientFundsException when bet exceeds the user's combined balance.
    Task<RoundOutcome> PlayRoundAsync(
        long userId,
        string gameKey,
        int bet,
        int payout,
        JsonDocument? details,
        CancellationToken ct);
}
