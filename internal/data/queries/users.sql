-- name: GetUserByHandle :one
-- handle column is citext, so comparison is case-insensitive at the DB level.
SELECT *
FROM users
WHERE handle = $1;

-- name: GetUserByID :one
SELECT *
FROM users
WHERE id = $1;

-- name: TouchUserLastSeen :exec
UPDATE users
SET last_seen_at = $2
WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = $2,
    password_algo = $3,
    password_updated_at = $4
WHERE id = $1;

-- name: UpdateUserProfile :exec
-- Updates every editable column from the TUI Profile screen in one round-trip.
-- The caller is expected to pass the full intended state (no partial updates);
-- nullable text columns map empty string → NULL via sqlc's *string overrides.
UPDATE users
SET real_name = $2,
    location = $3,
    bio = $4,
    time_zone_id = $5,
    temperature_unit = $6,
    clock_format = $7,
    date_format = $8,
    suppress_key_adoption_prompts = $9,
    require_ssh_key = $10
WHERE id = $1;

-- name: RenameUserHandle :exec
-- Owner-guarded handle rename. The unique index on users.handle is the
-- ultimate source of truth on collisions; the handler should pre-check
-- to surface a friendlier message but the DB still catches races.
UPDATE users
SET handle = $2
WHERE id = $1;

-- name: PromoteUserToSysop :exec
UPDATE users
SET is_sysop = TRUE
WHERE handle = $1
  AND is_sysop = FALSE;

-- name: GetOrCreateWallet :one
-- Upsert that returns the existing wallet or creates a zero-credit one. The
-- caller is responsible for the daily refresh check on the returned row.
INSERT INTO user_wallets (user_id, daily_credits, daily_credits_refreshed_on, winnings_balance, updated_at)
VALUES ($1, $2, $3, 0, $4)
ON CONFLICT (user_id) DO UPDATE
SET updated_at = user_wallets.updated_at
RETURNING *;

-- name: UpdateWallet :exec
UPDATE user_wallets
SET daily_credits = $2,
    daily_credits_refreshed_on = $3,
    winnings_balance = $4,
    updated_at = $5
WHERE user_id = $1;

-- name: InsertGameRound :exec
INSERT INTO game_rounds (user_id, game_key, bet, payout, net, details, played_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);
