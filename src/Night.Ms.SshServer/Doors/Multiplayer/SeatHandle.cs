namespace Night.Ms.SshServer.Doors.Multiplayer;

// Returned by ITableRegistry.SitDownAsync. The screen holds it while seated; on dispose it
// uses TableId + UserId to address SitOutAsync/StandUpAsync. ChatChannelId is part of the
// handle so the screen can spin up its chat pane without a second lookup.
public sealed record SeatHandle(
    long TableId,
    string GameKey,
    int SeatIndex,
    long UserId,
    long ChatChannelId,
    long StartingChips);

// Mirror for spectators. Same chat channel; no seat index.
public sealed record SpectatorHandle(
    long TableId,
    string GameKey,
    long UserId,
    long ChatChannelId);
