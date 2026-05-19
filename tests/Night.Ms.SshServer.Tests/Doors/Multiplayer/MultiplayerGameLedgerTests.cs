using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Multiplayer;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests.Doors.Multiplayer;

public class MultiplayerGameLedgerTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;

    public MultiplayerGameLedgerTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync() => _dbOptions = await _fixture.CreateFreshDatabaseAsync();
    public Task DisposeAsync() => Task.CompletedTask;

    private async Task<long> SeedUserAsync(string handle)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user.Id;
    }

    private MultiplayerGameLedger NewLedger(TimeProvider time, AppDbContext db) => new(db, time);

    private static JsonDocument SampleDetails() =>
        JsonDocument.Parse("""{"board":["As","Kd","2c","7h","Qs"],"actions":[]}""");

    [Fact]
    public async Task BuyIn_first_buy_in_debits_daily_first()
    {
        var userId = await SeedUserAsync("alice");
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).BuyInAsync(userId, "holdem", 200, default);

        Assert.Equal(200, outcome.Chips);
        Assert.Equal(WalletService.DailyAllotmentCoins - 200, outcome.Wallet.DailyCredits);
        Assert.Equal(0, outcome.Wallet.WinningsBalance);
    }

    [Fact]
    public async Task BuyIn_falls_through_to_winnings_when_daily_short()
    {
        var userId = await SeedUserAsync("bob");
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        await using (var seed = new AppDbContext(_dbOptions!))
        {
            seed.UserWallets.Add(new UserWallet
            {
                UserId = userId,
                DailyCredits = 100,
                DailyCreditsRefreshedOn = DateOnly.FromDateTime(time.GetUtcNow().UtcDateTime),
                WinningsBalance = 500,
                UpdatedAt = time.GetUtcNow(),
            });
            await seed.SaveChangesAsync();
        }

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).BuyInAsync(userId, "holdem", 300, default);

        Assert.Equal(0, outcome.Wallet.DailyCredits);
        Assert.Equal(300, outcome.Wallet.WinningsBalance); // 500 - 200 (the overflow after daily)
    }

    [Fact]
    public async Task BuyIn_throws_when_balance_insufficient()
    {
        var userId = await SeedUserAsync("carol");
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        await using (var seed = new AppDbContext(_dbOptions!))
        {
            seed.UserWallets.Add(new UserWallet
            {
                UserId = userId,
                DailyCredits = 50,
                DailyCreditsRefreshedOn = DateOnly.FromDateTime(time.GetUtcNow().UtcDateTime),
                WinningsBalance = 10,
                UpdatedAt = time.GetUtcNow(),
            });
            await seed.SaveChangesAsync();
        }

        await using var db = new AppDbContext(_dbOptions!);
        await Assert.ThrowsAsync<InsufficientFundsException>(() =>
            NewLedger(time, db).BuyInAsync(userId, "holdem", 200, default));
    }

    [Fact]
    public async Task CashOut_credits_winnings_not_daily()
    {
        var userId = await SeedUserAsync("dave");
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        await using (var seed = new AppDbContext(_dbOptions!))
        {
            seed.UserWallets.Add(new UserWallet
            {
                UserId = userId,
                DailyCredits = 0,
                DailyCreditsRefreshedOn = DateOnly.FromDateTime(time.GetUtcNow().UtcDateTime),
                WinningsBalance = 100,
                UpdatedAt = time.GetUtcNow(),
            });
            await seed.SaveChangesAsync();
        }

        await using var db = new AppDbContext(_dbOptions!);
        var snapshot = await NewLedger(time, db).CashOutAsync(userId, "holdem", 350, default);

        Assert.Equal(0, snapshot.DailyCredits);
        Assert.Equal(450, snapshot.WinningsBalance);
    }

    [Fact]
    public async Task SettleHand_writes_one_hand_row_and_per_human_game_round()
    {
        // Chip-stack model: wallets are NOT touched at settle. The wallet was debited at
        // BuyInAsync; per-hand wins/losses live in the chip stack until CashOutAsync.
        // SettleHand only writes the audit trail (parent hand row + per-player rounds).
        var aliceId = await SeedUserAsync("alice2");
        var bobId = await SeedUserAsync("bob2");
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        // Seed wallets at distinct balances so we can verify the ledger leaves them alone.
        await using (var seed = new AppDbContext(_dbOptions!))
        {
            seed.UserWallets.AddRange(
                new UserWallet { UserId = aliceId, WinningsBalance = 1000, UpdatedAt = time.GetUtcNow() },
                new UserWallet { UserId = bobId, WinningsBalance = 1000, UpdatedAt = time.GetUtcNow() });
            await seed.SaveChangesAsync();
        }

        var settle = new SettleHand(
            GameKey: "holdem",
            TableId: 1,
            HandNumber: 42,
            PlayerMovements:
            [
                new PlayerMovement(aliceId, "alice2", WageredThisHand: 50, Payout: 150, ChipStackAfter: 600),
                new PlayerMovement(bobId, "bob2", WageredThisHand: 100, Payout: 0, ChipStackAfter: 400),
                new PlayerMovement(null, "cpu:tight-tom", WageredThisHand: 30, Payout: 30, ChipStackAfter: 500),
            ],
            HandDetails: SampleDetails());

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).SettleHandAsync(settle, default);

        Assert.True(outcome.HandId > 0);
        Assert.Equal(2, outcome.RoundIds.Count); // CPU movement does not produce a round

        await using var verify = new AppDbContext(_dbOptions!);
        var hand = await verify.MultiplayerHands.SingleAsync();
        Assert.Equal("holdem", hand.GameKey);
        Assert.Equal(1, hand.TableId);
        Assert.Equal(42, hand.HandNo);

        var rounds = await verify.GameRounds.OrderBy(r => r.UserId).ToListAsync();
        Assert.Equal(2, rounds.Count);
        Assert.All(rounds, r => Assert.Equal(hand.Id, r.HandId));

        var alice = rounds.Single(r => r.UserId == aliceId);
        Assert.Equal(50, alice.Bet);
        Assert.Equal(150, alice.Payout);
        Assert.Equal(100, alice.Net);

        var bob = rounds.Single(r => r.UserId == bobId);
        Assert.Equal(100, bob.Bet);
        Assert.Equal(0, bob.Payout);
        Assert.Equal(-100, bob.Net);

        // Wallets unchanged — chips are in the seat, not the wallet.
        var wallets = await verify.UserWallets.OrderBy(w => w.UserId).ToListAsync();
        Assert.Equal(1000, wallets.Single(w => w.UserId == aliceId).WinningsBalance);
        Assert.Equal(1000, wallets.Single(w => w.UserId == bobId).WinningsBalance);
    }

    [Fact]
    public async Task SettleHand_skips_cpu_movements_entirely()
    {
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        // Hand consisting only of CPUs — ledger should insert the parent row but no
        // GameRounds and touch no wallets.
        var settle = new SettleHand(
            GameKey: "holdem",
            TableId: 1,
            HandNumber: 1,
            PlayerMovements:
            [
                new PlayerMovement(null, "cpu:loose-lucy", 50, 100, 500),
                new PlayerMovement(null, "cpu:tight-tom", 50, 0, 450),
            ],
            HandDetails: SampleDetails());

        await using var db = new AppDbContext(_dbOptions!);
        var outcome = await NewLedger(time, db).SettleHandAsync(settle, default);

        Assert.Empty(outcome.RoundIds);

        await using var verify = new AppDbContext(_dbOptions!);
        Assert.Single(await verify.MultiplayerHands.ToListAsync());
        Assert.Empty(await verify.GameRounds.ToListAsync());
        Assert.Empty(await verify.UserWallets.ToListAsync());
    }

    [Fact]
    public async Task SettleHand_unique_index_rejects_duplicate_hand_no_for_same_table()
    {
        var aliceId = await SeedUserAsync("alice3");
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 18, 10, 0, 0, TimeSpan.Zero));

        await using (var seed = new AppDbContext(_dbOptions!))
        {
            seed.UserWallets.Add(new UserWallet { UserId = aliceId, WinningsBalance = 500, UpdatedAt = time.GetUtcNow() });
            await seed.SaveChangesAsync();
        }

        var settle = new SettleHand(
            "holdem",
            TableId: 1,
            HandNumber: 7,
            PlayerMovements: [new PlayerMovement(aliceId, "alice3", 10, 20, 510)],
            HandDetails: SampleDetails());

        await using (var db = new AppDbContext(_dbOptions!))
        {
            await NewLedger(time, db).SettleHandAsync(settle, default);
        }

        // Same (gameKey, tableId, handNo) must collide via the unique index.
        await using (var db = new AppDbContext(_dbOptions!))
        {
            await Assert.ThrowsAnyAsync<DbUpdateException>(() =>
                NewLedger(time, db).SettleHandAsync(settle, default));
        }
    }
}
