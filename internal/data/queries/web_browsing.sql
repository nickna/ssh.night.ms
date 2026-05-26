-- Queries for the Web screen's per-user bookmarks + history. All scoped by
-- user_id; UNIQUE (user_id, url) on both tables turns "re-add" and "re-visit"
-- into upserts with no caller-side dedup.

-- name: ListWebBookmarks :many
SELECT id, url, title, sort_order, created_at, updated_at
FROM web_bookmarks
WHERE user_id = $1
ORDER BY sort_order ASC, created_at DESC;

-- name: AddWebBookmark :one
-- On conflict (already bookmarked) we refresh the title rather than no-op'ing,
-- so the user re-bookmarking from the URL row with a new title acts as a
-- rename. updated_at bumps so future sort-by-recency reorderings see it.
INSERT INTO web_bookmarks (user_id, url, title)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, url) DO UPDATE SET
    title      = EXCLUDED.title,
    updated_at = now()
RETURNING id, url, title, sort_order, created_at, updated_at;

-- name: RenameWebBookmark :exec
UPDATE web_bookmarks SET title = $3, updated_at = now()
WHERE user_id = $1 AND id = $2;

-- name: DeleteWebBookmark :exec
DELETE FROM web_bookmarks WHERE user_id = $1 AND id = $2;

-- name: RecentWebHistory :many
SELECT id, url, last_visited_at, visit_count
FROM web_history
WHERE user_id = $1
ORDER BY last_visited_at DESC
LIMIT $2;

-- name: RecordWebVisit :exec
INSERT INTO web_history (user_id, url)
VALUES ($1, $2)
ON CONFLICT (user_id, url) DO UPDATE SET
    last_visited_at = now(),
    visit_count     = web_history.visit_count + 1;

-- name: DeleteWebHistoryEntry :exec
DELETE FROM web_history WHERE user_id = $1 AND id = $2;

-- name: ClearWebHistory :exec
DELETE FROM web_history WHERE user_id = $1;

-- name: TrimWebHistory :exec
-- Keep only the most-recent N per user; called sampled (not every visit) to
-- bound the per-user row count over time.
DELETE FROM web_history h
WHERE h.user_id = $1 AND h.id NOT IN (
    SELECT id FROM web_history
    WHERE user_id = $1
    ORDER BY last_visited_at DESC
    LIMIT $2
);
