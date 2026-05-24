// seeduser creates a single user row for dev/test scenarios. Useful for
// dropping a known-credentials user into the schema for multi-user smoke
// tests (e.g., DMs between two handles) without going through the
// register screen.
//
// Idempotent: refuses to overwrite an existing handle.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/config"
)

func main() {
	var (
		handle   = flag.String("handle", "", "handle to create (required)")
		password = flag.String("password", "", "plaintext password (required)")
		isSysop  = flag.Bool("sysop", false, "make this user a sysop")
		conn     = flag.String("conn", "", "postgres connection string (defaults to NIGHTMS_DB_CONN)")
	)
	flag.Parse()
	if *handle == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "seeduser: -handle and -password required")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	connStr := *conn
	if connStr == "" {
		connStr = config.Load().DBConnStr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		logger.Error("connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	hasher := auth.NewHasher(auth.DefaultArgon2Params())
	hash, algo, err := hasher.Hash(*password)
	if err != nil {
		logger.Error("hash password", "err", err)
		os.Exit(1)
	}
	var algoPtr *string
	if algo != "" {
		algoPtr = &algo
	}

	now := time.Now().UTC()
	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("begin tx", "err", err)
		os.Exit(1)
	}
	defer tx.Rollback(ctx)

	var existingID int64
	err = tx.QueryRow(ctx, "SELECT id FROM users WHERE handle = $1", *handle).Scan(&existingID)
	if err == nil {
		fmt.Fprintf(os.Stderr, "seeduser: handle %q already exists as user_id=%d; refusing to overwrite\n",
			*handle, existingID)
		os.Exit(3)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		logger.Error("check existing", "err", err)
		os.Exit(1)
	}

	var newID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO users (
			handle, created_at, is_sysop, is_banned,
			clock_format, date_format, temperature_unit, time_zone_id, location_source,
			password_hash, password_algo, password_updated_at,
			suppress_key_adoption_prompts, require_ssh_key
		) VALUES (
			$1, $2, $3, FALSE,
			0, 0, 0, 'UTC', 0,
			$4, $5, $2,
			FALSE, FALSE
		) RETURNING id`,
		*handle, now, *isSysop, hash, algoPtr,
	).Scan(&newID)
	if err != nil {
		logger.Error("insert user", "err", err)
		os.Exit(1)
	}

	details, _ := json.Marshal(map[string]any{"handle": *handle, "is_sysop": *isSysop, "via": "seeduser"})
	if _, err := tx.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, target_type, target_id, details, created_at)
		 VALUES (NULL, 'user.seeded', 'user', $1, $2, $3)`,
		newID, details, now,
	); err != nil {
		logger.Error("audit insert", "err", err)
		os.Exit(1)
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("commit", "err", err)
		os.Exit(1)
	}
	fmt.Printf("created user_id=%d handle=%s sysop=%v\n", newID, *handle, *isSysop)
}
