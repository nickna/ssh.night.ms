using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests.Domain;

public class UserWatchlistItemTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _opts;

    public UserWatchlistItemTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync() => _opts = await _fixture.CreateFreshDatabaseAsync();
    public Task DisposeAsync() => Task.CompletedTask;

    private async Task<long> SeedUserAsync(string handle)
    {
        await using var db = new AppDbContext(_opts!);
        var u = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(u);
        await db.SaveChangesAsync();
        return u.Id;
    }

    [Fact]
    public async Task Can_persist_and_load_watchlist_item()
    {
        var userId = await SeedUserAsync("alice");
        await using (var db = new AppDbContext(_opts!))
        {
            db.UserWatchlistItems.Add(new UserWatchlistItem
            {
                UserId = userId,
                Symbol = "AAPL",
                Canonical = "AAPL",
                Kind = WatchlistKind.Stock,
                SortOrder = 0,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
        }
        await using (var db = new AppDbContext(_opts!))
        {
            var loaded = await db.UserWatchlistItems.SingleAsync(w => w.UserId == userId);
            Assert.Equal("AAPL", loaded.Symbol);
            Assert.Equal(WatchlistKind.Stock, loaded.Kind);
        }
    }

    [Fact]
    public async Task Unique_index_on_user_canonical_prevents_duplicates()
    {
        var userId = await SeedUserAsync("bob");
        await using (var db = new AppDbContext(_opts!))
        {
            db.UserWatchlistItems.Add(new UserWatchlistItem
            {
                UserId = userId, Symbol = "BTC", Canonical = "bitcoin",
                Kind = WatchlistKind.Crypto, SortOrder = 0, CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
        }
        await using (var db = new AppDbContext(_opts!))
        {
            db.UserWatchlistItems.Add(new UserWatchlistItem
            {
                UserId = userId, Symbol = "btc", Canonical = "bitcoin",
                Kind = WatchlistKind.Crypto, SortOrder = 1, CreatedAt = DateTimeOffset.UtcNow,
            });
            // Same (UserId, Canonical) → unique-index violation.
            await Assert.ThrowsAsync<DbUpdateException>(() => db.SaveChangesAsync());
        }
    }

    [Fact]
    public async Task Same_canonical_allowed_across_different_users()
    {
        var aliceId = await SeedUserAsync("alice2");
        var bobId = await SeedUserAsync("bob2");
        await using var db = new AppDbContext(_opts!);
        db.UserWatchlistItems.Add(new UserWatchlistItem
        {
            UserId = aliceId, Symbol = "AAPL", Canonical = "AAPL",
            Kind = WatchlistKind.Stock, SortOrder = 0, CreatedAt = DateTimeOffset.UtcNow,
        });
        db.UserWatchlistItems.Add(new UserWatchlistItem
        {
            UserId = bobId, Symbol = "AAPL", Canonical = "AAPL",
            Kind = WatchlistKind.Stock, SortOrder = 0, CreatedAt = DateTimeOffset.UtcNow,
        });
        await db.SaveChangesAsync();
        Assert.Equal(2, await db.UserWatchlistItems.CountAsync());
    }

    [Fact]
    public async Task Deleting_user_cascades_to_watchlist_rows()
    {
        var userId = await SeedUserAsync("carol");
        await using (var db = new AppDbContext(_opts!))
        {
            db.UserWatchlistItems.AddRange(
                new UserWatchlistItem { UserId = userId, Symbol = "AAPL", Canonical = "AAPL", Kind = WatchlistKind.Stock, SortOrder = 0, CreatedAt = DateTimeOffset.UtcNow },
                new UserWatchlistItem { UserId = userId, Symbol = "MSFT", Canonical = "MSFT", Kind = WatchlistKind.Stock, SortOrder = 1, CreatedAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();
        }
        await using (var db = new AppDbContext(_opts!))
        {
            var user = await db.Users.FindAsync(userId);
            db.Users.Remove(user!);
            await db.SaveChangesAsync();
        }
        await using (var db = new AppDbContext(_opts!))
        {
            Assert.Equal(0, await db.UserWatchlistItems.CountAsync(w => w.UserId == userId));
        }
    }
}
