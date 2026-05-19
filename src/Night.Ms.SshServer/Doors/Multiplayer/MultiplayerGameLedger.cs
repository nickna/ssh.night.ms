using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Doors.Multiplayer;

public sealed class MultiplayerGameLedger(AppDbContext db, TimeProvider time) : IMultiplayerGameLedger
{
    public async Task<BuyInOutcome> BuyInAsync(long userId, string gameKey, int buyInCoins, CancellationToken ct)
    {
        ArgumentOutOfRangeException.ThrowIfNegativeOrZero(buyInCoins);
        ArgumentException.ThrowIfNullOrEmpty(gameKey);

        await WalletService.EnsureWalletExistsAsync(db, userId, ct);

        await using var tx = await db.Database.BeginTransactionAsync(ct);

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
        if (buyInCoins > available)
        {
            await tx.RollbackAsync(ct);
            throw new InsufficientFundsException(buyInCoins, available);
        }

        var fromDaily = Math.Min(buyInCoins, wallet.DailyCredits);
        wallet.DailyCredits -= fromDaily;
        wallet.WinningsBalance -= buyInCoins - fromDaily;
        wallet.UpdatedAt = now;

        await db.SaveChangesAsync(ct);
        await tx.CommitAsync(ct);

        return new BuyInOutcome(
            buyInCoins,
            new WalletSnapshot(wallet.DailyCredits, wallet.WinningsBalance, WalletService.DailyAllotmentCoins));
    }

    public async Task<WalletSnapshot> CashOutAsync(long userId, string gameKey, int remainingChips, CancellationToken ct)
    {
        ArgumentOutOfRangeException.ThrowIfNegative(remainingChips);
        ArgumentException.ThrowIfNullOrEmpty(gameKey);

        await WalletService.EnsureWalletExistsAsync(db, userId, ct);

        await using var tx = await db.Database.BeginTransactionAsync(ct);

        var wallet = await db.UserWallets
            .FromSqlInterpolated($"SELECT * FROM user_wallets WHERE user_id = {userId} FOR UPDATE")
            .FirstAsync(ct);

        // Cash-out always lands in winnings, never refills daily — daily is a per-day
        // allotment, not a recharging bucket. A player who busts and rebuys gets 0 back;
        // a player who doubles up gets the gain on top of their pre-buy-in winnings.
        wallet.WinningsBalance += remainingChips;
        wallet.UpdatedAt = time.GetUtcNow();

        await db.SaveChangesAsync(ct);
        await tx.CommitAsync(ct);

        return new WalletSnapshot(wallet.DailyCredits, wallet.WinningsBalance, WalletService.DailyAllotmentCoins);
    }

    public async Task<SettleOutcome> SettleHandAsync(SettleHand request, CancellationToken ct)
    {
        ArgumentException.ThrowIfNullOrEmpty(request.GameKey);
        ArgumentNullException.ThrowIfNull(request.HandDetails);

        // Chip-stack model: the wallet is debited once at BuyInAsync and credited once at
        // CashOutAsync. Per-hand wins/losses live entirely in the seat's chip stack (held
        // in Redis + the engine's HoldemTableState). The ledger's job at hand-end is the
        // *audit trail* only — write the parent MultiplayerHand row and one GameRound per
        // human movement, so leaderboards and history queries have a per-hand record.
        // Wallets are not touched here.

        var humans = request.PlayerMovements
            .Where(p => p.UserId is not null)
            .OrderBy(p => p.UserId!.Value)
            .ToList();

        await using var tx = await db.Database.BeginTransactionAsync(ct);

        var now = time.GetUtcNow();
        var hand = new MultiplayerHand
        {
            GameKey = request.GameKey,
            TableId = request.TableId,
            HandNo = request.HandNumber,
            Details = request.HandDetails,
            SettledAt = now,
        };
        db.MultiplayerHands.Add(hand);
        await db.SaveChangesAsync(ct);

        var roundIds = new List<long>(humans.Count);

        foreach (var p in humans)
        {
            var net = p.Payout - p.WageredThisHand;
            var round = new GameRound
            {
                UserId = p.UserId!.Value,
                GameKey = request.GameKey,
                Bet = p.WageredThisHand,
                Payout = p.Payout,
                Net = net,
                Details = null, // heavy JSON lives once on the MultiplayerHand parent row
                PlayedAt = now,
                HandId = hand.Id,
            };
            db.GameRounds.Add(round);
            await db.SaveChangesAsync(ct);
            roundIds.Add(round.Id);
        }

        await tx.CommitAsync(ct);
        return new SettleOutcome(hand.Id, roundIds);
    }
}
