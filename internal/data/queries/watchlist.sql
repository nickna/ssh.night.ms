-- name: ListWatchlistItems :many
SELECT id, user_id, symbol, canonical, kind, sort_order, created_at
FROM user_watchlist_items
WHERE user_id = $1
ORDER BY sort_order ASC, id ASC;

-- name: CountWatchlistItems :one
SELECT COUNT(*) FROM user_watchlist_items WHERE user_id = $1;

-- name: AddWatchlistItem :one
-- The unique index (user_id, canonical) enforces no-dupes at the DB level; callers
-- must handle the constraint violation as "already on your watchlist".
INSERT INTO user_watchlist_items (user_id, symbol, canonical, kind, sort_order, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, user_id, symbol, canonical, kind, sort_order, created_at;

-- name: UpdateWatchlistItem :one
UPDATE user_watchlist_items
SET symbol = $3, canonical = $4, kind = $5
WHERE id = $1 AND user_id = $2
RETURNING id, user_id, symbol, canonical, kind, sort_order, created_at;

-- name: DeleteWatchlistItem :exec
DELETE FROM user_watchlist_items
WHERE id = $1 AND user_id = $2;

-- name: GetWatchlistItem :one
SELECT id, user_id, symbol, canonical, kind, sort_order, created_at
FROM user_watchlist_items
WHERE id = $1 AND user_id = $2;

-- name: SetWatchlistSortOrder :exec
-- Atomic-per-row sort_order write. The screen calls this twice inside a tx to
-- swap two rows for K/J reorder; the (user_id, sort_order) tuple isn't unique
-- so transient duplicates during the swap are fine.
UPDATE user_watchlist_items
SET sort_order = $3
WHERE id = $1 AND user_id = $2;
