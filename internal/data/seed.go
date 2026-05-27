package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// SeedDefaults ensures the default channels and forums exist. Idempotent,
// runs once at startup after migrations have completed.
func SeedDefaults(ctx context.Context, queries *gen.Queries, logger *slog.Logger) error {
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	if err := queries.SeedLobbyChannel(ctx, now); err != nil {
		return fmt.Errorf("seed: lobby channel: %w", err)
	}
	if err := queries.SeedGeneralForum(ctx); err != nil {
		return fmt.Errorf("seed: general forum: %w", err)
	}
	logger.Info("seed: defaults ensured (#lobby + General forum)")
	return nil
}
