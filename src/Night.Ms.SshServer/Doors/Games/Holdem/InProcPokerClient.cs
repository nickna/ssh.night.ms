using System.Text.Json;
using Night.Ms.SshServer.Doors.Multiplayer;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// In-process IPokerClient: forwards directly to the singleton ITableRegistry. When/if we
// horizontally scale, this gets replaced with a transport-aware client (HTTP, Redis stream,
// etc.) without the screen changing.
public sealed class InProcPokerClient(ITableRegistry registry, HoldemGame game) : IPokerClient
{
    private const string GameKey = "holdem";

    public async Task<TableHandle> GetDefaultTableAsync(CancellationToken ct)
    {
        var tableId = await registry.GetOrCreateDefaultTableAsync(GameKey, ct);
        // Spectate quietly creates the chat channel and coordinator, then hands the
        // channel id back — same path the screen uses on entry whether it ends up
        // seated or watching.
        var probe = await registry.SpectateAsync(GameKey, tableId, userId: 0, ct);
        return new TableHandle(
            tableId,
            GameKey,
            probe.ChatChannelId,
            game.SeatsPerTable,
            game.SmallBlind,
            game.BigBlind,
            game.MinBet,
            game.MaxBet);
    }

    public async Task<JsonDocument> GetSnapshotAsync(long tableId, long viewerUserId, CancellationToken ct)
    {
        var coordinator = await registry.GetOrStartCoordinatorAsync(GameKey, tableId, ct);
        return await coordinator.GetSnapshotAsync(viewerUserId, ct);
    }

    public Task<SeatHandle?> FindExistingSeatAsync(long userId, CancellationToken ct) =>
        registry.FindExistingSeatAsync(GameKey, userId, ct);

    public Task<MultiplayerOpResult> SitDownAsync(long tableId, long userId, string handle, long buyInChips, int? preferredSeat, CancellationToken ct) =>
        registry.SitDownAsync(GameKey, tableId, userId, handle, buyInChips, preferredSeat, ct);

    public async Task<MultiplayerOpResult> StandUpAsync(long tableId, long userId, CancellationToken ct)
    {
        var coordinator = await registry.GetOrStartCoordinatorAsync(GameKey, tableId, ct);
        return await coordinator.StandUpAsync(userId, ct);
    }

    public async Task<MultiplayerOpResult> SitOutAsync(long tableId, long userId, CancellationToken ct)
    {
        var coordinator = await registry.GetOrStartCoordinatorAsync(GameKey, tableId, ct);
        return await coordinator.SitOutAsync(userId, ct);
    }

    public async Task<MultiplayerOpResult> SubmitActionAsync(long tableId, long userId, string action, long amount, CancellationToken ct)
    {
        var coordinator = await registry.GetOrStartCoordinatorAsync(GameKey, tableId, ct);
        using var doc = JsonDocument.Parse(JsonSerializer.Serialize(new { action, amount }));
        return await coordinator.SubmitIntentAsync(userId, doc, ct);
    }

    public async Task HeartbeatAsync(long tableId, long userId, CancellationToken ct)
    {
        var coordinator = await registry.GetOrStartCoordinatorAsync(GameKey, tableId, ct);
        await coordinator.HeartbeatAsync(userId, ct);
    }
}
