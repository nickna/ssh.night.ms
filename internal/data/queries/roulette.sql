-- name: GetRouletteState :one
SELECT snapshot FROM roulette_state WHERE name = $1;

-- name: UpsertRouletteState :exec
INSERT INTO roulette_state (name, snapshot, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (name) DO UPDATE
SET snapshot = EXCLUDED.snapshot,
    updated_at = now();
