// Package data owns the Postgres connection pool, schema migrations, and the
// sqlc-generated query layer.
package data

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies any pending migrations to the target database.
//
// Cutover adoption: if the schema is already populated (e.g., a legacy stack
// migrated it previously) but golang-migrate has no schema_migrations row, we
// Force version 1 to adopt the existing tables in place. This avoids the
// "relation users already exists" error that would otherwise block startup on
// a pre-existing DB.
func RunMigrations(ctx context.Context, connStr string, logger *slog.Logger) error {
	bootstrapped, err := schemaAlreadyBootstrapped(ctx, connStr)
	if err != nil {
		return fmt.Errorf("migrations: detect existing schema: %w", err)
	}

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrations: open embedded source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, connStr)
	if err != nil {
		return fmt.Errorf("migrations: new migrator: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			logger.Warn("migrate source close", "err", srcErr)
		}
		if dbErr != nil {
			logger.Warn("migrate db close", "err", dbErr)
		}
	}()

	if bootstrapped {
		version, dirty, verErr := m.Version()
		switch {
		case errors.Is(verErr, migrate.ErrNilVersion):
			logger.Info("migrations: adopting pre-existing schema; force-marking version 1")
			if err := m.Force(1); err != nil {
				return fmt.Errorf("migrations: force adopt: %w", err)
			}
		case dirty:
			logger.Warn("migrations: clearing dirty state on existing schema", "version", version)
			if err := m.Force(int(version)); err != nil {
				return fmt.Errorf("migrations: force clean dirty: %w", err)
			}
		}
	}

	before, _, _ := m.Version()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrations: up: %w", err)
	}
	after, _, _ := m.Version()
	if before == after {
		logger.Info("migrations: up-to-date", "version", after)
	} else {
		logger.Info("migrations: applied", "from", before, "to", after)
	}
	return nil
}

// schemaAlreadyBootstrapped returns true if any of our owned tables exist in
// the public schema. We pick `users` as the sentinel because it appears in
// every migration set we'll ever ship — losing it means a wipe, in which case
// rerunning from scratch is the right behavior anyway.
func schemaAlreadyBootstrapped(ctx context.Context, connStr string) (bool, error) {
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'users'
		)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("query users existence: %w", err)
	}
	return exists, nil
}
