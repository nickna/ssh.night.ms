using System.Text.Json;
using Night.Ms.SshServer.Doors.Multiplayer;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Thin facade between HoldemScreen and the framework. The screen knows nothing about
// TableRegistry, coordinators, or Redis topics — it asks IPokerClient for a TableHandle,
// then sends actions and reads snapshots.
//
// All methods are async and may complete on a background thread. The screen marshals view
// updates via IApplication.Invoke regardless.
public interface IPokerClient
{
    // Returns the table the lobby's only Hold'em entry maps to. v1 ships with a single
    // fixed table (id=1, blinds 5/10, 6 max); the handle exposes the chat channel + blinds
    // so the screen can drive its setup without a second lookup.
    Task<TableHandle> GetDefaultTableAsync(CancellationToken ct);

    Task<JsonDocument> GetSnapshotAsync(long tableId, long viewerUserId, CancellationToken ct);

    // Returns this user's existing seat if they're already at a table (e.g. mid-hand
    // disconnect / reconnect from a different SSH session). Null when no seat exists; the
    // caller proceeds to the normal buy-in flow.
    Task<SeatHandle?> FindExistingSeatAsync(long userId, CancellationToken ct);

    Task<MultiplayerOpResult> SitDownAsync(
        long tableId,
        long userId,
        string handle,
        long buyInChips,
        int? preferredSeat,
        CancellationToken ct);

    Task<MultiplayerOpResult> StandUpAsync(long tableId, long userId, CancellationToken ct);
    Task<MultiplayerOpResult> SitOutAsync(long tableId, long userId, CancellationToken ct);

    Task<MultiplayerOpResult> SubmitActionAsync(
        long tableId,
        long userId,
        string action,
        long amount,
        CancellationToken ct);

    // Liveness ping; called every ~10s by the seated screen. Stops the coordinator's
    // abandoned-seat cleanup from cashing out the player while they're connected but idle.
    Task HeartbeatAsync(long tableId, long userId, CancellationToken ct);
}

public sealed record TableHandle(
    long TableId,
    string GameKey,
    long ChatChannelId,
    int MaxSeats,
    long SmallBlind,
    long BigBlind,
    long MinBuyIn,
    long MaxBuyIn);
