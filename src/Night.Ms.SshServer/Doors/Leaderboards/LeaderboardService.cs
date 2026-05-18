using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Doors.Leaderboards;

public sealed class LeaderboardService(AppDbContext db, TimeProvider time) : ILeaderboardService
{
    public async Task<IReadOnlyList<LeaderboardEntry>> GetTopSingleWinsAsync(int top, CancellationToken ct)
    {
        // ix_game_rounds_game_key_net covers (game_key, net desc) — Postgres can use it for
        // each game_key partition, then merge. Without the partial filter on net > 0 we'd
        // also surface uninteresting "biggest losses" at the bottom of the same index scan.
        var rows = await db.GameRounds
            .Where(r => r.Net > 0)
            .OrderByDescending(r => r.Net)
            .Take(top)
            .Select(r => new { r.Net, Handle = r.User!.Handle, r.GameKey, r.PlayedAt })
            .ToListAsync(ct);

        return rows
            .Select((x, i) => new LeaderboardEntry(i + 1, x.Handle, x.GameKey, x.Net, x.PlayedAt))
            .ToList();
    }

    public async Task<IReadOnlyList<LeaderboardEntry>> GetTopLifetimeNetAsync(int top, CancellationToken ct)
    {
        var rows = await db.GameRounds
            .GroupBy(r => r.UserId)
            .Select(g => new { UserId = g.Key, Total = g.Sum(r => (long)r.Net) })
            .OrderByDescending(g => g.Total)
            .Take(top)
            .Join(db.Users, g => g.UserId, u => u.Id, (g, u) => new { u.Handle, g.Total })
            .ToListAsync(ct);

        return rows
            .Select((x, i) => new LeaderboardEntry(i + 1, x.Handle, GameKey: string.Empty, x.Total, At: null))
            .ToList();
    }

    public async Task<IReadOnlyList<LeaderboardEntry>> GetHotStreaksAsync(int top, int sinceDays, CancellationToken ct)
    {
        var cutoff = time.GetUtcNow().AddDays(-sinceDays);

        var rows = await db.GameRounds
            .Where(r => r.PlayedAt > cutoff)
            .GroupBy(r => r.UserId)
            .Select(g => new { UserId = g.Key, Total = g.Sum(r => (long)r.Net) })
            .OrderByDescending(g => g.Total)
            .Take(top)
            .Join(db.Users, g => g.UserId, u => u.Id, (g, u) => new { u.Handle, g.Total })
            .ToListAsync(ct);

        return rows
            .Select((x, i) => new LeaderboardEntry(i + 1, x.Handle, GameKey: string.Empty, x.Total, At: null))
            .ToList();
    }
}
