namespace Night.Ms.SshServer.Doors.Games.Holdem.Events;

// Discriminator constants for PokerEventEnvelope. String constants rather than enum so the
// wire format is human-readable in redis-cli and easy to filter with rg.
public static class PokerEventKinds
{
    public const string HandStarted = "hand-started";
    public const string HoleCardsDealt = "hole-cards";        // private
    public const string BoardDealt = "board-dealt";
    public const string ActionTaken = "action";
    public const string StreetAdvanced = "street";
    public const string TurnStarted = "turn-started";
    public const string TimerTick = "timer-tick";
    public const string ShowdownStarted = "showdown";
    public const string HandEnded = "hand-ended";
    public const string SeatChanged = "seat";
    public const string SnapshotResync = "snapshot";
    public const string ActionRejected = "rejected";          // private
}
