-- name: ListUserSavedLocations :many
SELECT id, user_id, label, latitude, longitude, canonical, sort_order, created_at
FROM user_saved_locations
WHERE user_id = $1
ORDER BY sort_order ASC, id ASC;

-- name: GetPrimaryUserSavedLocation :one
-- Returns the first row by sort_order. Weather + Map screens consult this to
-- decide which lat/lon to load when no per-request override is supplied.
-- pgx.ErrNoRows means the user has nothing saved — caller falls back to the
-- env-var defaults.
SELECT id, user_id, label, latitude, longitude, canonical, sort_order, created_at
FROM user_saved_locations
WHERE user_id = $1
ORDER BY sort_order ASC, id ASC
LIMIT 1;

-- name: AddUserSavedLocation :one
-- The unique index (user_id, label) enforces no-dupes at the DB level; callers
-- must handle the constraint violation as "label already in use".
INSERT INTO user_saved_locations (user_id, label, latitude, longitude, canonical, sort_order, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, user_id, label, latitude, longitude, canonical, sort_order, created_at;

-- name: DeleteUserSavedLocation :exec
DELETE FROM user_saved_locations
WHERE id = $1 AND user_id = $2;

-- name: NextUserSavedLocationSortOrder :one
-- Returns the sort_order the next inserted row should get (max + 1, or 0).
-- Saves the caller a separate COUNT/MAX round trip.
SELECT COALESCE(MAX(sort_order) + 1, 0)::int AS next_sort_order
FROM user_saved_locations
WHERE user_id = $1;

-- name: RenameUserSavedLocation :exec
-- Owner-guarded rename. The unique (user_id, label) index catches
-- collisions; callers must handle the constraint violation as "label
-- already in use".
UPDATE user_saved_locations
SET label = $3
WHERE id = $1 AND user_id = $2;

-- name: SetUserSavedLocationSortOrder :exec
-- Atomic-per-row sort_order write. The Locations modal calls this twice
-- inside the same request to swap two rows for ↑/↓ reorder; the
-- (user_id, sort_order) tuple isn't unique so transient duplicates during
-- the swap are fine.
UPDATE user_saved_locations
SET sort_order = $3
WHERE id = $1 AND user_id = $2;
