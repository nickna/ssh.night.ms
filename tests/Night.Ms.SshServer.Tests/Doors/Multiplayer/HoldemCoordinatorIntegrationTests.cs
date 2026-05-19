using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Games.Holdem;
using Night.Ms.SshServer.Doors.Games.Holdem.Events;
using Night.Ms.SshServer.Doors.Multiplayer;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Tests.Doors.Multiplayer;

// End-to-end coordinator test: real Postgres, real Redis, real CPU AI. The screen layer
// is not involved — the test drives IPokerClient directly the way HoldemScreen does, so
// any regression in event ordering / settlement / ledger integration shows up here.
public class HoldemCoordinatorIntegrationTests : IClassFixture<PostgresFixture>, IClassFixture<RedisFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _pg;
    private readonly RedisFixture _redis;
    private ServiceProvider? _provider;
    private DbContextOptions<AppDbContext>? _dbOptions;

    public HoldemCoordinatorIntegrationTests(PostgresFixture pg, RedisFixture redis)
    {
        _pg = pg;
        _redis = redis;
    }

    public async Task InitializeAsync()
    {
        // Disable CPU think-time delays so the test's poll deadline holds. The delays are
        // a UX nicety for spectators; they aren't part of any behavior this test asserts.
        HoldemRules.CpuThinkMin = TimeSpan.Zero;
        HoldemRules.CpuThinkMax = TimeSpan.Zero;

        // Create a per-test database AND remember its connection string so DI can wire
        // against the same db EF migrations ran on.
        _dbOptions = await _pg.CreateFreshDatabaseAsync();
        var testCs = ExtractConnectionString(_dbOptions);

        // Flush Redis between test classes so seat hashes from prior runs don't bleed
        // into this one's reconciliation paths.
        await _redis.Multiplexer.GetServer(_redis.Multiplexer.GetEndPoints()[0]).FlushDatabaseAsync();

        var services = new ServiceCollection();
        services.AddLogging(b => b.SetMinimumLevel(LogLevel.Warning));
        services.AddSingleton(TimeProvider.System);
        services.AddSingleton(_redis.Multiplexer);
        services.AddSingleton<IRealtimeBus, RedisRealtimeBus>();
        services.AddSingleton<IGameRng, CryptoGameRng>();
        services.AddDbContext<AppDbContext>(o => o.UseNpgsql(testCs).UseSnakeCaseNamingConvention());
        services.AddDbContextFactory<AppDbContext>(o => o.UseNpgsql(testCs).UseSnakeCaseNamingConvention(), ServiceLifetime.Singleton);
        services.AddScoped<IMultiplayerGameLedger, MultiplayerGameLedger>();
        services.AddSingleton<ICpuPersonaRegistry, CpuPersonaRegistry>();
        services.AddSingleton<HoldemGame>();
        services.AddSingleton<ITableRegistry, TableRegistry>();
        services.AddSingleton<IPokerClient, InProcPokerClient>();

        _provider = services.BuildServiceProvider();
    }

    private static string ExtractConnectionString(DbContextOptions<AppDbContext> options)
    {
        var ext = options.Extensions
            .OfType<Microsoft.EntityFrameworkCore.Infrastructure.RelationalOptionsExtension>()
            .FirstOrDefault();
        if (ext?.ConnectionString is null)
            throw new InvalidOperationException("no connection string on options");
        return ext.ConnectionString;
    }

    public async Task DisposeAsync()
    {
        if (_provider is not null) await _provider.DisposeAsync();
    }

    private async Task<(long userId, IPokerClient client)> SetupSeatedUserAsync(string handle, long startingDaily = 500)
    {
        long userId;
        await using (var db = new AppDbContext(_dbOptions!))
        {
            var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
            db.Users.Add(user);
            await db.SaveChangesAsync();
            db.UserWallets.Add(new UserWallet
            {
                UserId = user.Id,
                DailyCredits = (int)startingDaily,
                DailyCreditsRefreshedOn = DateOnly.FromDateTime(DateTime.UtcNow),
                WinningsBalance = 0,
                UpdatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
            userId = user.Id;
        }
        return (userId, _provider!.GetRequiredService<IPokerClient>());
    }

    [Fact]
    public async Task SitDown_drives_a_hand_to_completion_and_writes_a_game_round()
    {
        var (userId, client) = await SetupSeatedUserAsync("alice");
        var handle = await client.GetDefaultTableAsync(default);
        Assert.Equal(1, handle.TableId);
        Assert.Equal("holdem", handle.GameKey);

        var result = await client.SitDownAsync(handle.TableId, userId, "alice", handle.MinBuyIn, preferredSeat: null, default);
        Assert.IsType<MultiplayerOpResult.Ok>(result);

        var mySeat = await client.FindExistingSeatAsync(userId, default);
        Assert.NotNull(mySeat);
        var mySeatIndex = mySeat.SeatIndex;

        // Snapshot-poll driven: when it's our turn, call (so we contribute chips and end
        // up in the settlement movements). When we see a settled hand row for our user,
        // we're done. Avoids the publish-then-await race entirely.
        var deadline = DateTime.UtcNow.AddSeconds(60);
        while (DateTime.UtcNow < deadline)
        {
            // Did the previous turn produce a settled round for us?
            await using (var verify = new AppDbContext(_dbOptions!))
            {
                var existing = await verify.GameRounds.AnyAsync(r => r.UserId == userId);
                if (existing) break;
            }

            using (var snap = await client.GetSnapshotAsync(handle.TableId, userId, default))
            {
                var dto = snap.RootElement.Deserialize<SnapshotResyncDto>();
                if (dto is not null && dto.ActorSeat == mySeatIndex)
                {
                    // Call (or check if free) to keep playing — this guarantees we have
                    // some TotalContribution by the time the hand settles, so the ledger
                    // emits a movement for our seat.
                    var action = dto.CurrentBet > 0 ? "call" : "check";
                    await client.SubmitActionAsync(handle.TableId, userId, action, 0, default);
                }
            }
            await Task.Delay(150);
        }

        // Settlement check: at least one multiplayer_hands row + one GameRound for the user.
        await using (var verify = new AppDbContext(_dbOptions!))
        {
            var hands = await verify.MultiplayerHands.ToListAsync();
            Assert.True(hands.Count >= 1, $"expected >=1 multiplayer_hands; got {hands.Count}");
            var rounds = await verify.GameRounds.Where(r => r.UserId == userId).ToListAsync();
            Assert.True(rounds.Count >= 1, $"expected >=1 game_round for user; got {rounds.Count}");
            Assert.NotNull(rounds[0].HandId);
            Assert.Contains(hands, h => h.Id == rounds[0].HandId);
        }
    }

}
