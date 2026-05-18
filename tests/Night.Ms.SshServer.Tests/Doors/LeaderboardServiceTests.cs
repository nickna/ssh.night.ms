using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors.Leaderboards;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests.Doors;

public class LeaderboardServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;

    public LeaderboardServiceTests(PostgresFixture fixture) => _fixture = fixture;

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

    private async Task SeedRoundAsync(long userId, string game, int bet, int payout, DateTimeOffset playedAt)
    {
        await using var db = new AppDbContext(_dbOptions!);
        db.GameRounds.Add(new GameRound
        {
            UserId = userId,
            GameKey = game,
            Bet = bet,
            Payout = payout,
            Net = payout - bet,
            Details = null,
            PlayedAt = playedAt,
        });
        await db.SaveChangesAsync();
    }

    [Fact]
    public async Task GetTopSingleWinsAsync_ranks_by_net_descending_and_excludes_losses()
    {
        var alice = await SeedUserAsync("alice");
        var bob = await SeedUserAsync("bob");
        var now = new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero);
        await SeedRoundAsync(alice, "slots", bet: 5, payout: 4000, playedAt: now);
        await SeedRoundAsync(bob, "videopoker", bet: 25, payout: 4000, playedAt: now);   // net 3975
        await SeedRoundAsync(alice, "slots", bet: 5, payout: 0, playedAt: now);          // loss — excluded

        await using var db = new AppDbContext(_dbOptions!);
        var svc = new LeaderboardService(db, new TestTimeProvider(now));

        var top = await svc.GetTopSingleWinsAsync(10, default);

        Assert.Equal(2, top.Count);
        Assert.Equal("alice", top[0].Handle);
        Assert.Equal(3995, top[0].Value);
        Assert.Equal("slots", top[0].GameKey);
        Assert.Equal(1, top[0].Rank);
        Assert.Equal("bob", top[1].Handle);
        Assert.Equal(3975, top[1].Value);
        Assert.Equal(2, top[1].Rank);
    }

    [Fact]
    public async Task GetTopLifetimeNetAsync_sums_across_games_per_user()
    {
        var alice = await SeedUserAsync("alice");
        var bob = await SeedUserAsync("bob");
        var now = new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero);
        // alice: +50 + (-10) = +40; bob: +100
        await SeedRoundAsync(alice, "slots", bet: 5, payout: 55, playedAt: now);
        await SeedRoundAsync(alice, "videopoker", bet: 25, payout: 15, playedAt: now);
        await SeedRoundAsync(bob, "slots", bet: 5, payout: 105, playedAt: now);

        await using var db = new AppDbContext(_dbOptions!);
        var svc = new LeaderboardService(db, new TestTimeProvider(now));

        var top = await svc.GetTopLifetimeNetAsync(10, default);

        Assert.Equal(2, top.Count);
        Assert.Equal("bob", top[0].Handle);
        Assert.Equal(100, top[0].Value);
        Assert.Equal("alice", top[1].Handle);
        Assert.Equal(40, top[1].Value);
    }

    [Fact]
    public async Task GetHotStreaksAsync_only_counts_rounds_inside_window()
    {
        var alice = await SeedUserAsync("alice");
        var now = new DateTimeOffset(2026, 5, 17, 10, 0, 0, TimeSpan.Zero);
        // Inside last 7 days: +200. Older than 7 days: +9999 (must not count).
        await SeedRoundAsync(alice, "slots", bet: 5, payout: 205, playedAt: now.AddDays(-1));
        await SeedRoundAsync(alice, "slots", bet: 5, payout: 10004, playedAt: now.AddDays(-30));

        await using var db = new AppDbContext(_dbOptions!);
        var svc = new LeaderboardService(db, new TestTimeProvider(now));

        var top = await svc.GetHotStreaksAsync(10, sinceDays: 7, default);

        var alicEntry = Assert.Single(top);
        Assert.Equal("alice", alicEntry.Handle);
        Assert.Equal(200, alicEntry.Value);
    }
}
