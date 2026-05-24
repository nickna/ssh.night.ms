package realtime

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// LeaderboardEntry is the screen-facing row. Rank is 1-based and assigned
// by the service in the order rows come back; the queries are already
// sorted, so this is just a numeric prefix for the renderer. For the
// single-win view, GameKey + At are populated; for cumulative views they're
// empty so the renderer can switch its column set.
type LeaderboardEntry struct {
	Rank    int
	Handle  string
	GameKey string
	Net     int64
	At      time.Time // zero for cumulative views
}

// LeaderboardService wraps the three GROUP-BY queries that drive the
// Leaderboards screen. All three return at most N rows (caller picks N).
// Empty results are not errors — the screen renders "(no rounds played
// yet)" when len() == 0.
type LeaderboardService struct {
	Queries *gen.Queries
}

// TopSingleWins returns the biggest per-round net wins across all games.
// Mirrors LeaderboardService.GetTopSingleWinsAsync in the .NET stack.
func (s *LeaderboardService) TopSingleWins(ctx context.Context, top int) ([]LeaderboardEntry, error) {
	rows, err := s.Queries.LeaderboardTopSingleWins(ctx, int32(top))
	if err != nil {
		return nil, fmt.Errorf("leaderboards: top single wins: %w", err)
	}
	out := make([]LeaderboardEntry, 0, len(rows))
	for i, r := range rows {
		out = append(out, LeaderboardEntry{
			Rank:    i + 1,
			Handle:  r.Handle,
			GameKey: r.GameKey,
			Net:     int64(r.Net),
			At:      r.PlayedAt.Time,
		})
	}
	return out, nil
}

// LifetimeNet returns the top-N users by sum(net) across every game_rounds
// row they've ever produced. Mirrors GetTopLifetimeNetAsync.
func (s *LeaderboardService) LifetimeNet(ctx context.Context, top int) ([]LeaderboardEntry, error) {
	rows, err := s.Queries.LeaderboardLifetimeNet(ctx, int32(top))
	if err != nil {
		return nil, fmt.Errorf("leaderboards: lifetime net: %w", err)
	}
	out := make([]LeaderboardEntry, 0, len(rows))
	for i, r := range rows {
		out = append(out, LeaderboardEntry{
			Rank:   i + 1,
			Handle: r.Handle,
			Net:    r.TotalNet,
		})
	}
	return out, nil
}

// HotStreaks returns the top-N users by sum(net) restricted to the last
// sinceDays days. Mirrors GetHotStreaksAsync.
func (s *LeaderboardService) HotStreaks(ctx context.Context, top, sinceDays int) ([]LeaderboardEntry, error) {
	cutoff := pgtype.Timestamptz{
		Time:  time.Now().UTC().AddDate(0, 0, -sinceDays),
		Valid: true,
	}
	rows, err := s.Queries.LeaderboardHotStreaks(ctx, gen.LeaderboardHotStreaksParams{
		PlayedAt: cutoff,
		Limit:    int32(top),
	})
	if err != nil {
		return nil, fmt.Errorf("leaderboards: hot streaks: %w", err)
	}
	out := make([]LeaderboardEntry, 0, len(rows))
	for i, r := range rows {
		out = append(out, LeaderboardEntry{
			Rank:   i + 1,
			Handle: r.Handle,
			Net:    r.TotalNet,
		})
	}
	return out, nil
}
