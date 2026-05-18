namespace Night.Ms.SshServer.Doors.Leaderboards;

public interface ILeaderboardService
{
    // Biggest single wins ever — one row per game round, ranked by net.
    Task<IReadOnlyList<LeaderboardEntry>> GetTopSingleWinsAsync(int top, CancellationToken ct);

    // Cumulative net across all rounds and all games — who's up the most lifetime?
    Task<IReadOnlyList<LeaderboardEntry>> GetTopLifetimeNetAsync(int top, CancellationToken ct);

    // Cumulative net in a recent rolling window. "Hot streaks" caption.
    Task<IReadOnlyList<LeaderboardEntry>> GetHotStreaksAsync(int top, int sinceDays, CancellationToken ct);
}
