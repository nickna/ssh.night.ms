-- name: AddBookmark :one
-- Insert a new bookmark, or refresh the title (and bump the implicit sort_order
-- back to the head) when the same (user_id, url) is added again. sort_order
-- auto-increments from the user's current max so freshly-added entries appear
-- at the bottom of ListBookmarks.
INSERT INTO user_bookmarks (user_id, url, title, sort_order, created_at)
VALUES (
    $1,
    $2,
    $3,
    COALESCE((SELECT MAX(sort_order) + 1 FROM user_bookmarks WHERE user_id = $1), 0),
    $4
)
ON CONFLICT (user_id, url) DO UPDATE
    SET title = EXCLUDED.title
RETURNING id, url, title, sort_order, created_at;

-- name: ListBookmarks :many
SELECT id, url, title, sort_order, created_at
FROM user_bookmarks
WHERE user_id = $1
ORDER BY sort_order ASC, created_at DESC
LIMIT $2;

-- name: DeleteBookmark :exec
DELETE FROM user_bookmarks
WHERE id = $1 AND user_id = $2;
