-- name: ListHoldemTables :many
SELECT id, name, cap_seats, small_blind, big_blind, snapshot, updated_at
FROM holdem_tables
ORDER BY id;

-- name: UpsertHoldemTable :exec
INSERT INTO holdem_tables (id, name, cap_seats, small_blind, big_blind, snapshot, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    cap_seats = EXCLUDED.cap_seats,
    small_blind = EXCLUDED.small_blind,
    big_blind = EXCLUDED.big_blind,
    snapshot = EXCLUDED.snapshot,
    updated_at = now();

-- name: DeleteHoldemTable :exec
DELETE FROM holdem_tables WHERE id = $1;
