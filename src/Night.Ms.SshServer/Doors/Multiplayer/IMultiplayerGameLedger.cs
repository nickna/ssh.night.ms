namespace Night.Ms.SshServer.Doors.Multiplayer;

// The wallet-aware operations a multiplayer door needs. Sits alongside the single-player
// IGameLedger — neither replaces the other; the single-player ledger keeps powering
// slots/blackjack/videopoker without churn.
//
// Atomicity guarantees:
//   - BuyInAsync locks one wallet FOR UPDATE; returns chips on success or
//     InsufficientChips on failure.
//   - CashOutAsync locks one wallet FOR UPDATE; credits chips to WinningsBalance.
//   - SettleHandAsync runs one transaction that inserts the parent MultiplayerHand row
//     then loops the player movements in ascending UserId order, locking each human's
//     wallet FOR UPDATE and inserting the GameRound row. Ascending order is the standard
//     deadlock-avoidance trick when two tables share players.
public interface IMultiplayerGameLedger
{
    Task<BuyInOutcome> BuyInAsync(long userId, string gameKey, int buyInCoins, CancellationToken ct);
    Task<WalletSnapshot> CashOutAsync(long userId, string gameKey, int remainingChips, CancellationToken ct);
    Task<SettleOutcome> SettleHandAsync(SettleHand request, CancellationToken ct);
}
