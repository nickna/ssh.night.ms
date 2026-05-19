using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Doors.Multiplayer;

// IHostedService that runs once at process start. Scans Redis for any seat hash entries
// whose lastSeen predates this boot — those are orphans left by an ungraceful shutdown
// (crash, kill -9). Credits the orphaned chips back to the user's WinningsBalance and
// removes the seat from Redis so the next sit-down starts fresh.
//
// Only humans (entries with userId != null) are reconciled — CPU chip stacks have no
// wallet to credit and just get dropped along with the seat hash.
//
// Scope: scans known game keys; "holdem" is the only one in v1 but the loop is structured
// so adding more games later is one line.
public sealed class TableReconciliationService(
    IConnectionMultiplexer redis,
    IServiceScopeFactory scopes,
    TimeProvider time,
    ILogger<TableReconciliationService> log) : IHostedService
{
    private static readonly string[] KnownGames = ["holdem"];

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        // Capture boot time before we touch Redis so any seat with lastSeen >= bootTime
        // is treated as "fresh after restart" (e.g. another instance still running) and
        // left alone. Single-process today; the comparison keeps multi-instance honest.
        var bootTime = time.GetUtcNow();

        foreach (var game in KnownGames)
        {
            try { await ReconcileGameAsync(game, bootTime, cancellationToken); }
            catch (Exception ex) { log.LogError(ex, "reconcile failed for game {Game}", game); }
        }
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    private async Task ReconcileGameAsync(string game, DateTimeOffset bootTime, CancellationToken ct)
    {
        var db = redis.GetDatabase();
        var server = GetAnyServer();
        if (server is null)
        {
            log.LogWarning("no Redis server available for reconciliation");
            return;
        }

        // SCAN every mpdoor:{game}:table:*:seats hash. Pattern matches our key layout from
        // MultiplayerTopics.SeatsKey; we don't track table ids elsewhere so SCAN is the
        // discovery mechanism.
        var pattern = $"mpdoor:{game}:table:*:seats";
        var orphansReclaimed = 0;
        await foreach (var key in server.KeysAsync(pattern: pattern).WithCancellation(ct))
        {
            try
            {
                var reclaimed = await ReconcileSeatsHashAsync(db, game, key.ToString(), bootTime, ct);
                orphansReclaimed += reclaimed;
            }
            catch (Exception ex) { log.LogError(ex, "reconcile failed for key {Key}", key); }
        }

        if (orphansReclaimed > 0)
            log.LogInformation("reconciled {Count} orphan seat(s) for {Game}", orphansReclaimed, game);
    }

    private IServer? GetAnyServer()
    {
        foreach (var ep in redis.GetEndPoints())
        {
            var server = redis.GetServer(ep);
            if (server.IsConnected) return server;
        }
        return null;
    }

    private async Task<int> ReconcileSeatsHashAsync(IDatabase db, string game, string key, DateTimeOffset bootTime, CancellationToken ct)
    {
        var entries = await db.HashGetAllAsync(key);
        var reclaimed = 0;
        foreach (var entry in entries)
        {
            try
            {
                using var json = JsonDocument.Parse(entry.Value.ToString());
                var root = json.RootElement;
                if (!root.TryGetProperty("userId", out var uidProp) || uidProp.ValueKind != JsonValueKind.Number)
                {
                    // CPU seat: drop the hash entry only. No wallet impact.
                    await db.HashDeleteAsync(key, entry.Name);
                    continue;
                }
                var userId = uidProp.GetInt64();
                var lastSeenUnix = root.TryGetProperty("lastSeen", out var ls) && ls.ValueKind == JsonValueKind.Number
                    ? ls.GetInt64() : 0L;
                var lastSeen = DateTimeOffset.FromUnixTimeSeconds(lastSeenUnix);
                if (lastSeen >= bootTime) continue;  // fresh — another instance owns it

                var chips = root.TryGetProperty("chips", out var c) && c.ValueKind == JsonValueKind.Number
                    ? (int)c.GetInt64() : 0;

                if (chips > 0)
                {
                    using var scope = scopes.CreateScope();
                    var ledger = scope.ServiceProvider.GetRequiredService<IMultiplayerGameLedger>();
                    await ledger.CashOutAsync(userId, game, chips, ct);
                }
                await db.HashDeleteAsync(key, entry.Name);
                await db.KeyDeleteAsync(MultiplayerTopics.UserSeatKey(game, userId));
                reclaimed++;
            }
            catch (Exception ex) { log.LogError(ex, "reconcile seat entry {Name} failed", entry.Name); }
        }
        return reclaimed;
    }
}
