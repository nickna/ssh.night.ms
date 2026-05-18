using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Doors;

public sealed class GameLedger(AppDbContext db, TimeProvider time) : IGameLedger
{
    public async Task<RoundOutcome> PlayRoundAsync(
        long userId,
        string gameKey,
        int bet,
        int payout,
        JsonDocument? details,
        CancellationToken ct)
    {
        ArgumentOutOfRangeException.ThrowIfNegative(bet);
        ArgumentOutOfRangeException.ThrowIfNegative(payout);
        ArgumentException.ThrowIfNullOrEmpty(gameKey);

        await WalletService.EnsureWalletExistsAsync(db, userId, ct);

        await using var tx = await db.Database.BeginTransactionAsync(ct);

        // FOR UPDATE serializes concurrent rounds for the same user behind a row lock.
        // EF tracks the returned entity so the in-memory mutations below flush as UPDATE.
        var wallet = await db.UserWallets
            .FromSqlInterpolated($"SELECT * FROM user_wallets WHERE user_id = {userId} FOR UPDATE")
            .FirstAsync(ct);

        var now = time.GetUtcNow();
        var today = DateOnly.FromDateTime(now.UtcDateTime);
        if (wallet.DailyCreditsRefreshedOn != today)
        {
            wallet.DailyCredits = WalletService.DailyAllotmentCoins;
            wallet.DailyCreditsRefreshedOn = today;
        }

        var available = (long)wallet.DailyCredits + wallet.WinningsBalance;
        if (bet > available)
        {
            await tx.RollbackAsync(ct);
            throw new InsufficientFundsException(bet, available);
        }

        var fromDaily = Math.Min(bet, wallet.DailyCredits);
        wallet.DailyCredits -= fromDaily;
        wallet.WinningsBalance -= bet - fromDaily;
        wallet.WinningsBalance += payout;
        wallet.UpdatedAt = now;

        var round = new GameRound
        {
            UserId = userId,
            GameKey = gameKey,
            Bet = bet,
            Payout = payout,
            Net = payout - bet,
            Details = details,
            PlayedAt = now,
        };
        db.GameRounds.Add(round);

        await db.SaveChangesAsync(ct);
        await tx.CommitAsync(ct);

        return new RoundOutcome(
            new WalletSnapshot(wallet.DailyCredits, wallet.WinningsBalance, WalletService.DailyAllotmentCoins),
            round.Id);
    }
}
