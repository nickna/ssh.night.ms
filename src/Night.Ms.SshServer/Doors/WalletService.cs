using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Doors;

public sealed class WalletService(AppDbContext db, TimeProvider time) : IWalletService
{
    // 500 coins/day was chosen so that with the bet ranges in place (slots 5-50, poker 5-25)
    // a user gets ~10-100 plays per day before they have to start spending their winnings.
    public const int DailyAllotmentCoins = 500;

    public async Task<WalletSnapshot> GetAsync(long userId, CancellationToken ct)
    {
        await EnsureWalletExistsAsync(db, userId, ct);

        var wallet = await db.UserWallets.FirstAsync(w => w.UserId == userId, ct);
        var today = DateOnly.FromDateTime(time.GetUtcNow().UtcDateTime);

        if (wallet.DailyCreditsRefreshedOn != today)
        {
            wallet.DailyCredits = DailyAllotmentCoins;
            wallet.DailyCreditsRefreshedOn = today;
            wallet.UpdatedAt = time.GetUtcNow();
            await db.SaveChangesAsync(ct);
        }

        return new WalletSnapshot(wallet.DailyCredits, wallet.WinningsBalance, DailyAllotmentCoins);
    }

    // Idempotent first-time creation. Run outside any wrapping transaction so concurrent
    // racers don't deadlock: one INSERT wins, the others are no-ops. After this returns,
    // the wallet row exists and is safe to lock with SELECT … FOR UPDATE.
    internal static Task EnsureWalletExistsAsync(AppDbContext db, long userId, CancellationToken ct) =>
        db.Database.ExecuteSqlInterpolatedAsync($@"
            INSERT INTO user_wallets (user_id, daily_credits, daily_credits_refreshed_on, winnings_balance, updated_at)
            VALUES ({userId}, 0, NULL, 0, NOW())
            ON CONFLICT (user_id) DO NOTHING", ct);
}
