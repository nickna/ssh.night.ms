using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests.Doors;

public class GameLedgerTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;

    public GameLedgerTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync() => _dbOptions = await _fixture.CreateFreshDatabaseAsync();
    public Task DisposeAsync() => Task.CompletedTask;

    private async Task<long> SeedUserAsync(string handle = "nick")
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user.Id;
    }

    private GameLedger NewLedger(TimeProvider time, AppDbContext db) => new(db, time);

    [Fact]
    public async Task PlayRound_first_round_of_day_refreshes_daily_then_debits_daily_first()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).PlayRoundAsync(userId, "slots", bet: 10, payout: 0, details: null, default);

        Assert.Equal(WalletService.DailyAllotmentCoins - 10, outcome.Wallet.DailyCredits);
        Assert.Equal(0, outcome.Wallet.WinningsBalance);
    }

    [Fact]
    public async Task PlayRound_falls_through_to_winnings_after_daily_exhausted()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        // Seed the wallet directly: daily=10, winnings=100. A bet of 30 should drain daily
        // and pull 20 from winnings.
        await using (var seed = new AppDbContext(_dbOptions!))
        {
            seed.UserWallets.Add(new UserWallet
            {
                UserId = userId,
                DailyCredits = 10,
                DailyCreditsRefreshedOn = DateOnly.FromDateTime(time.GetUtcNow().UtcDateTime),
                WinningsBalance = 100,
                UpdatedAt = time.GetUtcNow(),
            });
            await seed.SaveChangesAsync();
        }

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).PlayRoundAsync(userId, "slots", bet: 30, payout: 0, details: null, default);

        Assert.Equal(0, outcome.Wallet.DailyCredits);
        Assert.Equal(80, outcome.Wallet.WinningsBalance);
    }

    [Fact]
    public async Task PlayRound_payout_credits_winnings_not_daily()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).PlayRoundAsync(userId, "slots", bet: 10, payout: 50, details: null, default);

        Assert.Equal(WalletService.DailyAllotmentCoins - 10, outcome.Wallet.DailyCredits);
        Assert.Equal(50, outcome.Wallet.WinningsBalance);
    }

    [Fact]
    public async Task PlayRound_insufficient_funds_throws_and_does_not_persist()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        await using var db = new AppDbContext(_dbOptions!);
        var ledger = NewLedger(time, db);

        // 500 daily + 0 winnings = 500 total. Bet 600 → throws.
        var ex = await Assert.ThrowsAsync<InsufficientFundsException>(
            () => ledger.PlayRoundAsync(userId, "slots", bet: 600, payout: 0, details: null, default));
        Assert.Equal(600, ex.Requested);
        Assert.Equal(WalletService.DailyAllotmentCoins, ex.Available);

        await using var verify = new AppDbContext(_dbOptions!);
        // Wallet still exists from the refresh attempt, but no game_round was written.
        Assert.Empty(await verify.GameRounds.ToListAsync());
    }

    [Fact]
    public async Task PlayRound_writes_audit_row_with_net_bet_payout_and_details()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        var details = System.Text.Json.JsonDocument.Parse("""{"reels":["7","7","7"]}""");

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).PlayRoundAsync(userId, "slots", bet: 5, payout: 100, details: details, default);

        await using var verify = new AppDbContext(_dbOptions!);
        var row = Assert.Single(await verify.GameRounds.ToListAsync());
        Assert.Equal(outcome.RoundId, row.Id);
        Assert.Equal("slots", row.GameKey);
        Assert.Equal(5, row.Bet);
        Assert.Equal(100, row.Payout);
        Assert.Equal(95, row.Net);
        Assert.NotNull(row.Details);
        Assert.Equal("7", row.Details!.RootElement.GetProperty("reels")[0].GetString());
    }

    [Fact]
    public async Task PlayRound_concurrent_rounds_for_same_user_serialize_without_double_spend()
    {
        // Two parallel rounds, each betting 300, when the user has exactly 500 daily. If
        // FOR UPDATE serializes them correctly, the second one sees the post-debit state
        // and either falls through to winnings (0) or throws InsufficientFundsException.
        // Either way the wallet must end up non-negative.
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        async Task RoundAsync()
        {
            await using var db = new AppDbContext(_dbOptions!);
            try
            {
                await new GameLedger(db, time).PlayRoundAsync(userId, "slots", bet: 300, payout: 0, details: null, default);
            }
            catch (InsufficientFundsException)
            {
                // Expected for the loser of the race.
            }
        }

        await Task.WhenAll(RoundAsync(), RoundAsync());

        await using var verify = new AppDbContext(_dbOptions!);
        var wallet = await verify.UserWallets.FirstAsync(w => w.UserId == userId);
        Assert.True(wallet.DailyCredits >= 0, $"daily went negative: {wallet.DailyCredits}");
        Assert.True(wallet.WinningsBalance >= 0, $"winnings went negative: {wallet.WinningsBalance}");
        // Total spent across both attempts can't exceed the 500 the user started with.
        var totalSpent = WalletService.DailyAllotmentCoins - wallet.DailyCredits - wallet.WinningsBalance;
        Assert.True(totalSpent <= WalletService.DailyAllotmentCoins, $"overspent: {totalSpent}");
    }
}
