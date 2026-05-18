using System.Text.Json;

namespace Night.Ms.SshServer.Domain;

// One row per completed bet (a slot spin, a poker hand, etc.). Powers leaderboards and audit.
// Net = Payout - Bet, stored explicitly so leaderboard aggregations don't have to compute it
// in every query. Details is game-specific (reel symbols, dealt+held cards, final hand rank);
// keeping it jsonb means the schema doesn't grow per game.
public sealed class GameRound
{
    public long Id { get; set; }
    public long UserId { get; set; }
    public required string GameKey { get; set; }
    public int Bet { get; set; }
    public int Payout { get; set; }
    public int Net { get; set; }
    public JsonDocument? Details { get; set; }
    public DateTimeOffset PlayedAt { get; set; }

    public User? User { get; set; }
}
