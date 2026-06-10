package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapSysop is the idempotent escape-hatch for promoting a sysop at
// startup. Reads NIGHTMS_BOOTSTRAP_SYSOP_HANDLE (and optionally _PASSWORD); if
// present:
//
//   - Only HANDLE set, user exists → promote IsSysop to true (no-op if already).
//   - Only HANDLE set, user missing → no-op (register flow will promote).
//   - HANDLE + PASSWORD set, user missing → create with seeded password + sysop.
//   - HANDLE + PASSWORD set, user exists with no password → seed the password.
//   - User exists with password already set → NEVER overwrite (idempotent).
//
// All writes record an audit_log row. Runs once at boot, before the SSH
// listener accepts connections.
func BootstrapSysop(
	ctx context.Context,
	pool *pgxpool.Pool,
	hasher *Hasher,
	handle, password string,
	logger *slog.Logger,
) error {
	if handle == "" {
		return nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sysop bootstrap: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		userID  int64
		isSysop bool
		hasPwd  bool
	)
	err = tx.QueryRow(ctx,
		`SELECT id, is_sysop, password_hash IS NOT NULL FROM users WHERE handle = $1`,
		handle).Scan(&userID, &isSysop, &hasPwd)

	now := time.Now().UTC()

	if errors.Is(err, pgx.ErrNoRows) {
		if password == "" {
			logger.Info("sysop handle not yet registered; register flow will promote",
				"handle", handle)
			return tx.Commit(ctx)
		}
		// Bootstrap-with-password path: stand up the sysop account from scratch.
		fresh, algo, hashErr := hasher.Hash(password)
		if hashErr != nil {
			return fmt.Errorf("sysop bootstrap: hash password: %w", hashErr)
		}
		var algoPtr *string
		if algo != "" {
			algoPtr = &algo
		}
		var insertedID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO users (
				handle, created_at, is_sysop, is_banned,
				clock_format, date_format, temperature_unit, time_zone_id, location_source,
				password_hash, password_algo, password_updated_at,
				suppress_key_adoption_prompts, require_ssh_key
			) VALUES (
				$1, $2, TRUE, FALSE,
				0, 0, 0, 'UTC', 0,
				$3, $4, $2,
				FALSE, FALSE
			) RETURNING id`,
			handle, now, fresh, algoPtr,
		).Scan(&insertedID); err != nil {
			return fmt.Errorf("sysop bootstrap: insert user: %w", err)
		}
		if err := insertAuditLog(ctx, tx, nil, "sysop.bootstrap_seeded", "user", &insertedID,
			map[string]any{"handle": handle, "with_password": true}, now); err != nil {
			return err
		}
		logger.Info("bootstrapped sysop user from env vars with seeded password",
			"handle", handle, "user_id", insertedID)
		return tx.Commit(ctx)
	}
	if err != nil {
		return fmt.Errorf("sysop bootstrap: select existing user: %w", err)
	}

	changed := false

	if !isSysop {
		if _, err := tx.Exec(ctx, `UPDATE users SET is_sysop = TRUE WHERE id = $1`, userID); err != nil {
			return fmt.Errorf("sysop bootstrap: promote: %w", err)
		}
		if err := insertAuditLog(ctx, tx, nil, "sysop.bootstrap", "user", &userID,
			map[string]any{"handle": handle}, now); err != nil {
			return err
		}
		logger.Info("promoted to sysop via env var", "handle", handle, "user_id", userID)
		changed = true
	}

	if password != "" && !hasPwd {
		fresh, algo, hashErr := hasher.Hash(password)
		if hashErr != nil {
			return fmt.Errorf("sysop bootstrap: hash password: %w", hashErr)
		}
		var algoPtr *string
		if algo != "" {
			algoPtr = &algo
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET password_hash = $1, password_algo = $2, password_updated_at = $3 WHERE id = $4`,
			fresh, algoPtr, now, userID); err != nil {
			return fmt.Errorf("sysop bootstrap: seed password: %w", err)
		}
		if err := insertAuditLog(ctx, tx, nil, "sysop.bootstrap_seeded_password", "user", &userID,
			map[string]any{"handle": handle}, now); err != nil {
			return err
		}
		logger.Info("seeded initial password for sysop", "handle", handle, "user_id", userID)
		changed = true
	}

	if !changed {
		logger.Debug("sysop bootstrap: no-op", "handle", handle)
	}
	return tx.Commit(ctx)
}

func insertAuditLog(ctx context.Context, tx pgx.Tx, actorID *int64, action, targetType string, targetID *int64, details map[string]any, at time.Time) error {
	body, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("audit: marshal details: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, target_type, target_id, details, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		actorID, action, targetType, targetID, body, pgtype.Timestamptz{Time: at, Valid: true},
	)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}
