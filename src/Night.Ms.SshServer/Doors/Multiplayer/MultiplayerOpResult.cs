namespace Night.Ms.SshServer.Doors.Multiplayer;

// Result type for sit-down / stand-up / submit-action / etc. Discriminated rather than
// throwing because most of these "failures" are flow-control (seat full, insufficient
// funds, not your turn) that the screen needs to surface inline, not stack traces.
public abstract record MultiplayerOpResult
{
    public sealed record Ok : MultiplayerOpResult;
    public sealed record Rejected(string Reason) : MultiplayerOpResult;
    public sealed record SeatFull : MultiplayerOpResult;
    public sealed record InsufficientChips(long Need, long Have) : MultiplayerOpResult;
    public sealed record AlreadySeated(long TableId, int SeatIndex) : MultiplayerOpResult;
    public sealed record NotYourTurn : MultiplayerOpResult;
}
