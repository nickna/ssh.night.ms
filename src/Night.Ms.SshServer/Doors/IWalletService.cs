namespace Night.Ms.SshServer.Doors;

public interface IWalletService
{
    // Reads (and lazily refreshes) the user's wallet. On the first read of each UTC day the
    // daily bucket is topped up to DailyAllotmentCoins — without this the only way to refill
    // would be to actually play, which feels broken on a fresh login.
    Task<WalletSnapshot> GetAsync(long userId, CancellationToken ct);
}
