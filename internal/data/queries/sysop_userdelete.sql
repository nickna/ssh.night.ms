-- name: GetOrCreateSentinelUser :one
-- Returns the id of the '[deleted]' sentinel user, lazily creating it on
-- first call. The handle uses square brackets which IsValidHandle rejects,
-- so no real user can ever own this row. The sentinel is banned and has no
-- password / no credentials so it can never log in.
INSERT INTO users (handle, created_at, is_sysop, is_banned)
VALUES ('[deleted]', now(), false, true)
ON CONFLICT (handle) DO UPDATE SET handle = users.handle
RETURNING id;

-- name: CountUserContent :one
-- Pre-flight summary shown in the delete-user confirm modal so the sysop
-- sees the blast radius before authorizing.
SELECT
  (SELECT COUNT(*) FROM chat_messages WHERE user_id = $1)::bigint        AS chat_count,
  (SELECT COUNT(*) FROM topics       WHERE created_by_id = $1)::bigint   AS topic_count,
  (SELECT COUNT(*) FROM posts        WHERE created_by_id = $1)::bigint   AS post_count;

-- name: ReassignChatMessagesAuthor :exec
-- Three ON DELETE RESTRICT FKs (chat_messages, topics, posts) block a hard
-- DELETE FROM users. We re-point them at the sentinel inside a tx, then
-- delete the user; CASCADE cleans up the rest.
UPDATE chat_messages SET user_id = $2 WHERE user_id = $1;

-- name: ReassignTopicsAuthor :exec
UPDATE topics SET created_by_id = $2 WHERE created_by_id = $1;

-- name: ReassignPostsAuthor :exec
UPDATE posts SET created_by_id = $2 WHERE created_by_id = $1;

-- name: DeleteUserByID :exec
DELETE FROM users WHERE id = $1;
