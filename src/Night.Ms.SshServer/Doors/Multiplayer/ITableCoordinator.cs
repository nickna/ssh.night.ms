using System.Text.Json;

namespace Night.Ms.SshServer.Doors.Multiplayer;

// One per active table. Single-writer for the table's authoritative state in Redis: every
// intent flows through here in order via the XREAD intents stream, every event is published
// by here on the events topic. The coordinator interface intentionally treats engine state
// and intents as opaque JSON so the framework doesn't know Hold'em-specific shapes.
//
// Concrete coordinators live under each game's folder (Doors/Games/Holdem/...). The
// framework only needs the lifecycle hooks here.
public interface ITableCoordinator : IAsyncDisposable
{
    long TableId { get; }
    string GameKey { get; }
    bool IsRunning { get; }

    Task StartAsync(CancellationToken ct);
    Task StopAsync(CancellationToken ct);

    // Snapshot for a particular viewer: hole cards masked for everyone except the viewer.
    // Returns the JSON the events topic also emits for SnapshotResync.
    Task<JsonDocument> GetSnapshotAsync(long viewerUserId, CancellationToken ct);

    // Intent submission. The screen serializes its action into a JSON envelope and hands it
    // to the coordinator, which either acknowledges acceptance into the mailbox (Ok) or
    // refuses synchronously for cheap-to-detect errors (NotYourTurn, Rejected). The actual
    // rule application happens on the coordinator's intent loop; the bus echo is what
    // updates the screen.
    Task<MultiplayerOpResult> SubmitIntentAsync(long userId, JsonDocument intent, CancellationToken ct);

    // Sit-down / stand-up / sit-out forwarded by the registry. The coordinator updates the
    // seats hash and publishes a SeatChanged event so other sessions update their views.
    Task<MultiplayerOpResult> SitDownAsync(long userId, string handle, long startingChips, int? preferredSeat, CancellationToken ct);
    Task<MultiplayerOpResult> StandUpAsync(long userId, CancellationToken ct);
    Task<MultiplayerOpResult> SitOutAsync(long userId, CancellationToken ct);
    Task<MultiplayerOpResult> ResumeAsync(long userId, CancellationToken ct);

    // Liveness ping from a seated session. Bumps lastSeenAt so the coordinator's clock
    // loop doesn't cash this player out as abandoned. Sessions should call this every
    // ~10s; missing for >5min triggers auto-cashout.
    Task HeartbeatAsync(long userId, CancellationToken ct);
}
