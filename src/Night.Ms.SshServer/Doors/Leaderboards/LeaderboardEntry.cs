namespace Night.Ms.SshServer.Doors.Leaderboards;

// A single row in any leaderboard view. GameKey is empty for aggregated lifetime/streak
// rows where the total spans every game; populated for "top single wins" where the row
// refers to one specific round.
public sealed record LeaderboardEntry(
    int Rank,
    string Handle,
    string GameKey,
    long Value,
    DateTimeOffset? At);
