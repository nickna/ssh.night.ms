-- name: GetChannelByName :one
-- name is citext, so comparison is case-insensitive at the DB level.
SELECT *
FROM channels
WHERE name = $1;

-- name: GetChannelByID :one
SELECT *
FROM channels
WHERE id = $1;

-- name: CreateChannel :one
INSERT INTO channels (name, topic, is_private, created_by_id, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: RecentMessagesForChannel :many
-- Pulls the most recent N messages for a channel, joined with the author handle
-- so the renderer doesn't have to round-trip per-message. Returns newest-first;
-- the screen reverses for chronological display.
SELECT m.id,
       m.channel_id,
       m.user_id,
       m.body,
       m.created_at,
       m.edited_at,
       m.deleted_at,
       m.is_pinned,
       m.parent_message_id,
       u.handle AS author_handle,
       u.is_sysop AS author_is_sysop
FROM chat_messages m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.deleted_at IS NULL
ORDER BY m.created_at DESC
LIMIT $2;

-- name: InsertChatMessage :one
-- parent_message_id is nullable: top-level messages pass NULL, replies pass
-- the root message's id. FK enforces RESTRICT so replies can't dangle.
INSERT INTO chat_messages (channel_id, user_id, body, created_at, is_pinned, parent_message_id)
VALUES ($1, $2, $3, $4, FALSE, $5)
RETURNING id, channel_id, user_id, body, created_at, parent_message_id;

-- name: JoinChannelMembership :exec
-- Idempotent join. Used by /join; future code paths (auto-join on first
-- message, sysop invite) reuse the same query.
INSERT INTO channel_members (channel_id, user_id, joined_at, role)
VALUES ($1, $2, $3, 'member')
ON CONFLICT DO NOTHING;

-- name: LeaveChannelMembership :exec
DELETE FROM channel_members
WHERE channel_id = $1 AND user_id = $2;

-- name: ListChannelsForUser :many
-- Returns channels the user is a member of, in stable order. #lobby sorts to
-- the top because it's the canonical default; otherwise alphabetical by name.
SELECT c.id, c.name, c.topic, c.is_private, c.created_by_id, c.created_at
FROM channel_members m
JOIN channels c ON c.id = m.channel_id
WHERE m.user_id = $1
ORDER BY (c.name = 'lobby') DESC, c.name ASC;

-- name: UpdateChatMessage :one
-- Authoritative edit. user_id guard means the SQL refuses to edit someone
-- else's message even if the screen passes a stale id; we don't have to
-- trust the caller to filter. Returns the new edited_at so the publisher
-- carries an accurate "edited at HH:MM" hint downstream.
UPDATE chat_messages
SET body = $3, edited_at = $4
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING id, channel_id, user_id, body, created_at, edited_at;

-- name: GetLastOwnMessageInChannel :one
-- Used by /edit to find the message to rewrite. Returns the user's most
-- recent non-deleted message in the channel.
SELECT id, channel_id, user_id, body, created_at, edited_at
FROM chat_messages
WHERE channel_id = $1 AND user_id = $2 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: UpsertChannelRead :exec
-- Mark a channel as "read up to message N" for the given user. Idempotent;
-- never moves the marker backward (so a stale write doesn't reset progress).
INSERT INTO channel_reads (user_id, channel_id, last_read_message_id, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, channel_id) DO UPDATE
SET last_read_message_id = GREATEST(channel_reads.last_read_message_id, EXCLUDED.last_read_message_id),
    updated_at = EXCLUDED.updated_at;

-- name: UnreadCountsForUser :many
-- Returns one row per channel the user is a member of, with the count of
-- non-deleted messages newer than last_read_message_id. Channels with no
-- channel_reads row default last_read to 0 so freshly-joined channels start
-- with every message marked unread.
SELECT m.channel_id,
       COUNT(cm.id)::bigint AS unread
FROM channel_members m
LEFT JOIN channel_reads r
  ON r.user_id = m.user_id AND r.channel_id = m.channel_id
LEFT JOIN chat_messages cm
  ON cm.channel_id = m.channel_id
  AND cm.id > COALESCE(r.last_read_message_id, 0)
  AND cm.deleted_at IS NULL
WHERE m.user_id = $1
GROUP BY m.channel_id;

-- name: LatestMessageIDInChannel :one
-- Returns 0 when no messages exist (NULL → COALESCE).
SELECT COALESCE(MAX(id), 0)::bigint AS id
FROM chat_messages
WHERE channel_id = $1 AND deleted_at IS NULL;

-- name: AddReaction :exec
-- Idempotent: one (message, user, emoji) tuple per row, ON CONFLICT lets
-- the same user mash the same emoji repeatedly without inflating the count.
INSERT INTO message_reactions (message_id, user_id, emoji, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING;

-- name: RemoveReaction :exec
DELETE FROM message_reactions
WHERE message_id = $1 AND user_id = $2 AND emoji = $3;

-- name: ReactionsForChannel :many
-- Aggregates reactions across every message in the channel — bootstrap calls
-- this once so the chat log can render existing reaction chips on first paint.
SELECT mr.message_id,
       mr.emoji,
       COUNT(*)::bigint AS n
FROM message_reactions mr
JOIN chat_messages cm ON cm.id = mr.message_id
WHERE cm.channel_id = $1 AND cm.deleted_at IS NULL
GROUP BY mr.message_id, mr.emoji
ORDER BY mr.message_id, mr.emoji;

-- name: SeedLobbyChannel :exec
-- Idempotent seed of the default public channel — used by the startup
-- initializer on every boot. The ON CONFLICT clause is a guard for cutover
-- scenarios where multiple stacks may have touched the DB at different times.
INSERT INTO channels (name, topic, is_private, created_at)
VALUES ('lobby', 'general chat', FALSE, $1)
ON CONFLICT DO NOTHING;

-- name: GetChatMessageByID :one
-- Author + state lookup for authz on mutating ops (delete/pin/unpin).
SELECT id, channel_id, user_id, body, created_at, edited_at, deleted_at, is_pinned, parent_message_id
FROM chat_messages
WHERE id = $1;

-- name: SoftDeleteChatMessage :exec
-- Author-only delete. The user_id guard refuses someone else's row even if
-- the caller passes a stale id; sysop deletes go through the sysop variant.
UPDATE chat_messages
SET deleted_at = $3
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteChatMessageAsSysop :exec
-- Sysop-bypass variant. The caller must verify the actor's is_sysop bit
-- before invoking this query.
UPDATE chat_messages
SET deleted_at = $2
WHERE id = $1 AND deleted_at IS NULL;

-- name: SetChatMessagePinned :exec
-- Toggles the is_pinned flag. Caller has already verified the message isn't
-- deleted and that the actor is authorized for this channel.
UPDATE chat_messages
SET is_pinned = $2
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListPinnedMessagesForChannel :many
-- Cap at 50 — pinning is for "the few things worth keeping," not a backlog.
SELECT m.id,
       m.channel_id,
       m.user_id,
       m.body,
       m.created_at,
       m.edited_at,
       m.deleted_at,
       m.is_pinned,
       m.parent_message_id,
       u.handle AS author_handle,
       u.is_sysop AS author_is_sysop
FROM chat_messages m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.is_pinned = TRUE
  AND m.deleted_at IS NULL
ORDER BY m.created_at DESC
LIMIT 50;

-- name: SetChannelTopic :exec
-- NULL clears the topic; channel-creator authz lives in the service layer.
UPDATE channels
SET topic = $2
WHERE id = $1;

-- name: SearchChatMessagesFTS :many
-- Full-text search over the generated body_search tsvector. websearch_to_tsquery
-- never throws on malformed input but returns an empty query for tokens it
-- can't parse (very short or punctuation-only); callers fall back to ILIKE in
-- that case.
SELECT m.id,
       m.channel_id,
       m.user_id,
       m.body,
       m.created_at,
       m.edited_at,
       m.deleted_at,
       m.is_pinned,
       m.parent_message_id,
       u.handle AS author_handle,
       u.is_sysop AS author_is_sysop
FROM chat_messages m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.deleted_at IS NULL
  AND m.body_search @@ websearch_to_tsquery('english', $2)
ORDER BY m.created_at DESC
LIMIT $3;

-- name: SearchChatMessagesILike :many
-- Fallback for short/punctuation-only queries the tsquery parser drops. The
-- pattern includes a leading + trailing % so it matches anywhere in body.
SELECT m.id,
       m.channel_id,
       m.user_id,
       m.body,
       m.created_at,
       m.edited_at,
       m.deleted_at,
       m.is_pinned,
       m.parent_message_id,
       u.handle AS author_handle,
       u.is_sysop AS author_is_sysop
FROM chat_messages m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.deleted_at IS NULL
  AND m.body ILIKE $2
ORDER BY m.created_at DESC
LIMIT $3;

-- name: ReplyCountsForParents :many
-- Per-parent reply count for the messages currently loaded into the chat
-- log. Caller passes the visible message ids and the SQL groups children
-- whose parent_message_id falls into that set. Deleted children don't count.
SELECT parent_message_id,
       COUNT(*)::bigint AS n
FROM chat_messages
WHERE parent_message_id IS NOT NULL
  AND parent_message_id = ANY($1::bigint[])
  AND deleted_at IS NULL
GROUP BY parent_message_id;

-- name: BatchHasPfpByHandles :many
-- Bulk "does this handle have a profile picture uploaded?" lookup. citext
-- comparison is case-insensitive at the DB level so the caller can pass mixed
-- case; we return handle::text so the row keys are plain strings.
SELECT handle::text AS handle,
       (profile_picture_updated_at IS NOT NULL) AS has_pfp
FROM users
WHERE handle = ANY($1::citext[]);

-- name: ChatMessageCountForUser :one
SELECT COUNT(*)::bigint AS n
FROM chat_messages
WHERE user_id = $1 AND deleted_at IS NULL;

-- name: TopicCountForUser :one
SELECT COUNT(*)::bigint AS n
FROM topics
WHERE created_by_id = $1;

-- name: PostCountForUser :one
SELECT COUNT(*)::bigint AS n
FROM posts
WHERE created_by_id = $1;
