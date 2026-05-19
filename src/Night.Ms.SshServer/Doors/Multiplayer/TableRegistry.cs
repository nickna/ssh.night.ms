using System.Collections.Concurrent;
using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors.Games.Holdem;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Doors.Multiplayer;

// Singleton coordinator registry. Lazy-creates one coordinator per (gameKey, tableId);
// concurrent first-touchers see the same instance via Lazy<Task<>> double-checked init.
// Today v1 supports one fixed table per game (id=1); the lobby/discovery surface stays
// the same when multi-table lands.
public sealed class TableRegistry(
    IServiceProvider services,
    IConnectionMultiplexer redis,
    IRealtimeBus bus,
    ICpuPersonaRegistry personas,
    IGameRng rng,
    TimeProvider time,
    ILoggerFactory loggerFactory,
    IDbContextFactory<AppDbContext> dbFactory,
    ILogger<TableRegistry> log) : ITableRegistry
{
    private readonly ConcurrentDictionary<(string Game, long Table), Lazy<Task<ITableCoordinator>>> _coordinators = new();

    public Task<long> GetOrCreateDefaultTableAsync(string gameKey, CancellationToken ct) =>
        // v1: every game gets exactly one table with id=1. Adding tiered stakes later
        // means generating distinct table ids here; clients still call this with just
        // the gameKey to find "any open table."
        Task.FromResult(1L);

    public Task<IReadOnlyList<TableSummary>> ListTablesAsync(string gameKey, CancellationToken ct) =>
        // v1: hardcoded list of one. Will become an enumeration of the coordinators dict
        // once multi-table support lands.
        Task.FromResult<IReadOnlyList<TableSummary>>(Array.Empty<TableSummary>());

    public async Task<SeatHandle?> FindExistingSeatAsync(string gameKey, long userId, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var tableIdRaw = await db.StringGetAsync(MultiplayerTopics.UserSeatKey(gameKey, userId));
        if (!tableIdRaw.HasValue) return null;
        if (!long.TryParse(tableIdRaw.ToString(), out var tableId)) return null;

        var seats = await db.HashGetAllAsync(MultiplayerTopics.SeatsKey(gameKey, tableId));
        foreach (var entry in seats)
        {
            using var json = JsonDocument.Parse(entry.Value.ToString());
            var root = json.RootElement;
            if (!root.TryGetProperty("userId", out var uid)) continue;
            if (uid.ValueKind != JsonValueKind.Number || uid.GetInt64() != userId) continue;
            var seatIndex = int.Parse(entry.Name.ToString());
            var chips = root.GetProperty("chips").GetInt64();
            var chatChannelId = await ResolveChatChannelIdAsync(gameKey, tableId, ct);
            return new SeatHandle(tableId, gameKey, seatIndex, userId, chatChannelId, chips);
        }
        return null;
    }

    public async Task<MultiplayerOpResult> SitDownAsync(
        string gameKey,
        long tableId,
        long userId,
        string handle,
        long buyInChips,
        int? preferredSeat,
        CancellationToken ct)
    {
        // Debit the wallet first so a failed buy-in never reaches the coordinator. If the
        // coordinator's SitDownAsync later rejects (e.g. seat full), we cash-out the chips
        // back to the wallet so the player's bankroll is unchanged.
        using var scope = services.CreateScope();
        var ledger = scope.ServiceProvider.GetRequiredService<IMultiplayerGameLedger>();
        BuyInOutcome buyIn;
        try
        {
            buyIn = await ledger.BuyInAsync(userId, gameKey, (int)buyInChips, ct);
        }
        catch (Doors.InsufficientFundsException ex)
        {
            return new MultiplayerOpResult.InsufficientChips(ex.Requested, ex.Available);
        }

        var coordinator = await GetOrStartCoordinatorAsync(gameKey, tableId, ct);
        var sitResult = await coordinator.SitDownAsync(userId, handle, buyIn.Chips, preferredSeat, ct);
        if (sitResult is not MultiplayerOpResult.Ok)
        {
            // Refund: push chips back into the wallet so the user isn't out money on a
            // rejected sit-down. CashOut credits winnings rather than daily but that's the
            // right thing here — the daily allowance isn't refundable.
            try { await ledger.CashOutAsync(userId, gameKey, (int)buyIn.Chips, ct); }
            catch (Exception ex) { log.LogError(ex, "refund-after-failed-sit failed user={UserId}", userId); }
        }
        return sitResult;
    }

    public async Task<SpectatorHandle> SpectateAsync(string gameKey, long tableId, long userId, CancellationToken ct)
    {
        // Ensure the coordinator (and chat channel) exist so spectators have something to
        // attach to. No bus/persistence work here — spectating is a screen-side concern.
        _ = await GetOrStartCoordinatorAsync(gameKey, tableId, ct);
        var chatChannelId = await ResolveChatChannelIdAsync(gameKey, tableId, ct);
        return new SpectatorHandle(tableId, gameKey, userId, chatChannelId);
    }

    public Task<ITableCoordinator> GetOrStartCoordinatorAsync(string gameKey, long tableId, CancellationToken ct)
    {
        var key = (gameKey, tableId);
        var lazy = _coordinators.GetOrAdd(key, _ => new Lazy<Task<ITableCoordinator>>(() => CreateCoordinatorAsync(gameKey, tableId, ct)));
        return lazy.Value;
    }

    public async Task StopAllAsync(CancellationToken ct)
    {
        // Snapshot the dictionary first — coordinator.StopAsync may mutate it (via
        // SeatChanged → CPU floor etc.), and we don't want to enumerate during mutation.
        var snapshot = _coordinators.Values.ToList();
        foreach (var lazy in snapshot)
        {
            if (!lazy.IsValueCreated) continue;
            try
            {
                var coordinator = await lazy.Value;
                await coordinator.StopAsync(ct);
                await coordinator.DisposeAsync();
            }
            catch (Exception ex)
            {
                log.LogError(ex, "failed to stop coordinator");
            }
        }
        _coordinators.Clear();
    }

    private async Task<ITableCoordinator> CreateCoordinatorAsync(string gameKey, long tableId, CancellationToken ct)
    {
        var chatChannelId = await ResolveChatChannelIdAsync(gameKey, tableId, ct);
        ITableCoordinator coordinator = gameKey switch
        {
            "holdem" => new HoldemTableCoordinator(
                tableId,
                chatChannelId,
                redis,
                bus,
                services,
                personas,
                rng,
                time,
                loggerFactory.CreateLogger<HoldemTableCoordinator>()),
            _ => throw new NotSupportedException($"no coordinator for game '{gameKey}'"),
        };
        await coordinator.StartAsync(ct);
        return coordinator;
    }

    // -- Chat channel provisioning ----------------------------------------------------

    // The chat channel for a multiplayer table is a real Channel row so the existing chat
    // infra (ChatMutationService, channel reads, etc.) Just Works™. We create it lazily
    // on first table touch — no need to register all tables ahead of time.
    private async Task<long> ResolveChatChannelIdAsync(string gameKey, long tableId, CancellationToken ct)
    {
        var name = $"poker-table-{tableId}";
        await using var db = await dbFactory.CreateDbContextAsync(ct);
        var existing = await db.Channels.FirstOrDefaultAsync(c => c.Name == name, ct);
        if (existing is not null) return existing.Id;

        var channel = new Channel
        {
            Name = name,
            Topic = $"{gameKey} table {tableId}",
            IsPrivate = false,
            CreatedAt = time.GetUtcNow(),
        };
        db.Channels.Add(channel);
        try
        {
            await db.SaveChangesAsync(ct);
        }
        catch (DbUpdateException)
        {
            db.ChangeTracker.Clear();
            existing = await db.Channels.FirstOrDefaultAsync(c => c.Name == name, ct);
            if (existing is null) throw;
            return existing.Id;
        }
        return channel.Id;
    }
}
