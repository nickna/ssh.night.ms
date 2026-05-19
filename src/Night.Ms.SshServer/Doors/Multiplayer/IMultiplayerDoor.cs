namespace Night.Ms.SshServer.Doors.Multiplayer;

// Marker for door games that seat multiple users at a shared table. Discovered via the same
// services.GetServices<IDoorGame>() path as single-player doors, so DoorsScreen lists them
// alongside slots/blackjack. The extra metadata (seats, buy-in range, table count) is what
// the lobby and the framework's CPU filler need to size things correctly.
//
// MinBet / MaxBet on the base IDoorGame are reinterpreted here as the buy-in range in CHIPS
// (not wallet coins): the door wallet exchanges coins → chips at sit-down at a 1:1 rate.
public interface IMultiplayerDoor : IDoorGame
{
    int MaxConcurrentTables { get; }
    int SeatsPerTable { get; }
    int MinSeatedToStart { get; }
    long SmallBlind { get; }
    long BigBlind { get; }
}
