namespace Night.Ms.SshServer.Doors.Multiplayer;

// Singleton; the lobby's entry point into multiplayer doors. Owns the in-memory dictionary
// of live coordinators (one per active table) plus the Redis-side reverse-lookup keys that
// let a reconnecting player find their existing seat without scanning every table.
//
// Registry methods are engine-agnostic. The concrete coordinator implementation per game is
// resolved through DI: ITableRegistry asks the IMultiplayerDoor whose Key matches to spin
// one up the first time a table is touched.
public interface ITableRegistry
{
    Task<IReadOnlyList<TableSummary>> ListTablesAsync(string gameKey, CancellationToken ct);

    // Idempotent table provisioning. v1 has a single hard-coded table per game; later
    // versions can branch on stakes/format. Returns a stable tableId the coordinator owns.
    Task<long> GetOrCreateDefaultTableAsync(string gameKey, CancellationToken ct);

    // O(1) lookup of "is this user already seated somewhere in this game". Returns null if
    // they aren't. Used by the screen on entry to skip the buy-in modal and resume.
    Task<SeatHandle?> FindExistingSeatAsync(string gameKey, long userId, CancellationToken ct);

    Task<MultiplayerOpResult> SitDownAsync(
        string gameKey,
        long tableId,
        long userId,
        string handle,
        long buyInChips,
        int? preferredSeat,
        CancellationToken ct);

    Task<SpectatorHandle> SpectateAsync(string gameKey, long tableId, long userId, CancellationToken ct);

    // Internal accessor used by InProcPokerClient to forward intents to the right
    // coordinator. Starts a coordinator if none is running for the table yet.
    Task<ITableCoordinator> GetOrStartCoordinatorAsync(string gameKey, long tableId, CancellationToken ct);

    // Stops every active coordinator and cashes out their seated humans. Called by
    // TableShutdownService on ApplicationStopping.
    Task StopAllAsync(CancellationToken ct);
}
