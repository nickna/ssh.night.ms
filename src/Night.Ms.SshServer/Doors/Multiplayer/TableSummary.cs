namespace Night.Ms.SshServer.Doors.Multiplayer;

// What the lobby grid shows for a table without paying the cost of a full snapshot fetch.
public sealed record TableSummary(
    long TableId,
    string GameKey,
    int SeatsTotal,
    int SeatsFilled,
    int HumansSeated,
    int Spectators,
    long ChatChannelId,
    long SmallBlind,
    long BigBlind);
