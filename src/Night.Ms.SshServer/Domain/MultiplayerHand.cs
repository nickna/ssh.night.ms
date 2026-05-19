using System.Text.Json;

namespace Night.Ms.SshServer.Domain;

// One row per settled hand in a multiplayer door (Hold'em today, future seated games later).
// Owns the heavy per-hand JSON (board cards, action history, payouts) so it isn't duplicated
// onto each player's GameRound row. Player-level economic outcome stays on GameRound; rows
// reference this via the optional GameRound.HandId FK.
public sealed class MultiplayerHand
{
    public long Id { get; set; }
    public required string GameKey { get; set; }
    public long TableId { get; set; }
    public long HandNo { get; set; }
    public required JsonDocument Details { get; set; }
    public DateTimeOffset SettledAt { get; set; }
}
