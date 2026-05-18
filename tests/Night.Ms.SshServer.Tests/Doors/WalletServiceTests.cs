using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests.Doors;

public class WalletServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;

    public WalletServiceTests(PostgresFixture fixture) => _fixture = fixture;

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

    [Fact]
    public async Task GetAsync_first_call_creates_wallet_with_full_daily_allotment()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        await using var db = new AppDbContext(_dbOptions!);
        var sut = new WalletService(db, time);

        var snapshot = await sut.GetAsync(userId, default);

        Assert.Equal(WalletService.DailyAllotmentCoins, snapshot.DailyCredits);
        Assert.Equal(0, snapshot.WinningsBalance);
        Assert.Equal(WalletService.DailyAllotmentCoins, snapshot.DailyAllotment);
    }

    [Fact]
    public async Task GetAsync_second_call_same_day_returns_persisted_state()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero));

        await using (var db = new AppDbContext(_dbOptions!))
        {
            var sut = new WalletService(db, time);
            await sut.GetAsync(userId, default);

            // Mutate wallet directly to verify the second GetAsync does NOT clobber state.
            var wallet = await db.UserWallets.FirstAsync(w => w.UserId == userId);
            wallet.DailyCredits = 123;
            wallet.WinningsBalance = 456;
            await db.SaveChangesAsync();
        }

        time.Advance(TimeSpan.FromHours(3));

        await using var db2 = new AppDbContext(_dbOptions!);
        var sut2 = new WalletService(db2, time);
        var snapshot = await sut2.GetAsync(userId, default);

        Assert.Equal(123, snapshot.DailyCredits);
        Assert.Equal(456, snapshot.WinningsBalance);
    }

    [Fact]
    public async Task GetAsync_next_utc_day_refreshes_daily_but_preserves_winnings()
    {
        var userId = await SeedUserAsync();
        var time = new TestTimeProvider(new DateTimeOffset(2026, 5, 17, 23, 30, 0, TimeSpan.Zero));

        await using (var db = new AppDbContext(_dbOptions!))
        {
            var sut = new WalletService(db, time);
            await sut.GetAsync(userId, default);
            var wallet = await db.UserWallets.FirstAsync(w => w.UserId == userId);
            wallet.DailyCredits = 0;       // simulate fully-spent daily bucket
            wallet.WinningsBalance = 999;  // simulate accumulated winnings
            await db.SaveChangesAsync();
        }

        time.SetUtcNow(new DateTimeOffset(2026, 5, 18, 0, 30, 0, TimeSpan.Zero));

        await using var db2 = new AppDbContext(_dbOptions!);
        var sut2 = new WalletService(db2, time);
        var snapshot = await sut2.GetAsync(userId, default);

        Assert.Equal(WalletService.DailyAllotmentCoins, snapshot.DailyCredits);
        Assert.Equal(999, snapshot.WinningsBalance);
    }
}
