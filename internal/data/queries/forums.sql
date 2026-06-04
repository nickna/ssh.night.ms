-- name: ListForums :many
SELECT id, name, description, sort_order, topic_count, last_activity_at
FROM forums
ORDER BY sort_order, name;

-- name: GetForumByID :one
SELECT id, name, description, sort_order, topic_count, last_activity_at
FROM forums
WHERE id = $1;

-- name: ListTopicsInForum :many
-- One row per topic, with the author handle and post count denormalized so
-- the topic list paints with one round-trip.
SELECT t.id,
       t.forum_id,
       t.title,
       t.created_by_id,
       t.created_at,
       t.last_post_at,
       u.handle AS author_handle,
       COALESCE(pc.post_count, 0)::bigint AS post_count
FROM topics t
JOIN users u ON u.id = t.created_by_id
LEFT JOIN (
    SELECT topic_id, COUNT(*) AS post_count
    FROM posts
    GROUP BY topic_id
) pc ON pc.topic_id = t.id
WHERE t.forum_id = $1
ORDER BY t.last_post_at DESC
LIMIT $2;

-- name: ListTopicsInForumPaged :many
-- Same shape as ListTopicsInForum but with an OFFSET so the web forum view
-- can page through topics. Total count for the page controls comes from the
-- denormalized forums.topic_count, so no separate COUNT query is needed.
SELECT t.id,
       t.forum_id,
       t.title,
       t.created_by_id,
       t.created_at,
       t.last_post_at,
       u.handle AS author_handle,
       COALESCE(pc.post_count, 0)::bigint AS post_count
FROM topics t
JOIN users u ON u.id = t.created_by_id
LEFT JOIN (
    SELECT topic_id, COUNT(*) AS post_count
    FROM posts
    GROUP BY topic_id
) pc ON pc.topic_id = t.id
WHERE t.forum_id = $1
ORDER BY t.last_post_at DESC
LIMIT $2 OFFSET $3;

-- name: GetTopicByID :one
SELECT id, forum_id, title, created_by_id, created_at, last_post_at
FROM topics
WHERE id = $1;

-- name: ListPostsInTopic :many
-- Joined with the author handle so the thread renderer doesn't have to
-- chase a per-post lookup.
SELECT p.id,
       p.topic_id,
       p.parent_post_id,
       p.body,
       p.created_by_id,
       p.created_at,
       p.edited_at,
       u.handle AS author_handle,
       u.is_sysop AS author_is_sysop
FROM posts p
JOIN users u ON u.id = p.created_by_id
WHERE p.topic_id = $1
ORDER BY p.created_at;

-- name: ListPostsInTopicPaged :many
-- Same shape as ListPostsInTopic but with LIMIT/OFFSET so the web thread
-- view can page through long threads. Total count for the page controls
-- comes from the topic's denormalized post_count (the topic-list query),
-- so no separate COUNT query is needed.
SELECT p.id,
       p.topic_id,
       p.parent_post_id,
       p.body,
       p.created_by_id,
       p.created_at,
       p.edited_at,
       u.handle AS author_handle,
       u.is_sysop AS author_is_sysop
FROM posts p
JOIN users u ON u.id = p.created_by_id
WHERE p.topic_id = $1
ORDER BY p.created_at
LIMIT $2 OFFSET $3;

-- name: CreateTopic :one
-- Topic body lives in the first post; this only creates the topic shell.
-- last_post_at is set to created_at so the topic immediately sorts to the
-- top of the forum list.
INSERT INTO topics (forum_id, title, created_by_id, created_at, last_post_at)
VALUES ($1, $2, $3, $4, $4)
RETURNING id, forum_id, title, created_by_id, created_at, last_post_at;

-- name: CreatePost :one
INSERT INTO posts (topic_id, parent_post_id, body, created_by_id, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, topic_id, parent_post_id, body, created_by_id, created_at, edited_at;

-- name: TouchTopicLastPost :exec
-- Bump last_post_at so the topic moves to the top of the forum list after
-- a new post. Idempotent + monotonic-ish (we always set to now-on-create).
UPDATE topics SET last_post_at = $2 WHERE id = $1;

-- name: IncrementForumTopicCount :exec
-- Used on CreateTopic. The COALESCE on last_activity_at handles the case
-- where the previous value is in the future (which shouldn't happen, but
-- GREATEST keeps the column monotonic regardless).
UPDATE forums
SET topic_count = topic_count + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, $2), $2)
WHERE id = $1;

-- name: TouchForumLastActivity :exec
-- Used on Reply. Monotonic via GREATEST so an out-of-order write can't
-- rewind the column.
UPDATE forums
SET last_activity_at = GREATEST(COALESCE(last_activity_at, $2), $2)
WHERE id = $1;

-- name: LatestPostInTopic :one
-- Returns id = 0 / zero-time when no posts exist (NULL → COALESCE). Used
-- right before UpsertPostRead so the caller knows what marker to write.
-- The MAX(created_at) is cast to timestamptz so sqlc emits a typed pgtype
-- field instead of falling through to interface{}.
SELECT COALESCE(MAX(id), 0)::bigint AS id,
       MAX(created_at)::timestamptz AS created_at
FROM posts
WHERE topic_id = $1;

-- name: UpsertPostRead :exec
-- Mark a topic as "read up to post N" for the given user. Idempotent and
-- monotonic in both columns (GREATEST never rewinds). Mirrors UpsertChannelRead.
INSERT INTO post_reads (user_id, topic_id, last_read_at, last_read_post_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, topic_id) DO UPDATE
SET last_read_at = GREATEST(post_reads.last_read_at, EXCLUDED.last_read_at),
    last_read_post_id = GREATEST(
        COALESCE(post_reads.last_read_post_id, 0),
        COALESCE(EXCLUDED.last_read_post_id, 0));

-- name: UnreadTopicCountsForForum :many
-- One row per topic in the forum, with the count of posts whose id is
-- strictly greater than the user's last_read_post_id marker. Topics with
-- no post_reads row default to 0 so the topic shows every post as unread.
SELECT t.id AS topic_id,
       COUNT(p.id)::bigint AS unread
FROM topics t
LEFT JOIN post_reads r
  ON r.user_id = $1 AND r.topic_id = t.id
LEFT JOIN posts p
  ON p.topic_id = t.id
  AND p.id > COALESCE(r.last_read_post_id, 0)
WHERE t.forum_id = $2
GROUP BY t.id;

-- name: UnreadCountsByForumForUser :many
-- Same shape as UnreadTopicCountsForForum but aggregated across the whole
-- DB so the forum list can paint per-forum unread badges with one query.
SELECT t.forum_id,
       COUNT(p.id)::bigint AS unread
FROM topics t
LEFT JOIN post_reads r
  ON r.user_id = $1 AND r.topic_id = t.id
LEFT JOIN posts p
  ON p.topic_id = t.id
  AND p.id > COALESCE(r.last_read_post_id, 0)
GROUP BY t.forum_id;

-- name: SeedGeneralForum :exec
-- Idempotent seed of the default forum.
INSERT INTO forums (name, description, sort_order)
VALUES ('General', 'general discussion', 0)
ON CONFLICT DO NOTHING;
