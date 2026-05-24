-- Boards visual refactor: denormalize forum activity onto forums.* so the
-- forum list can render topic counts + last-activity without an aggregate
-- join, and add post_reads.last_read_post_id so unread tracking is race-free
-- (the existing last_read_at column races with same-second posts).

ALTER TABLE public.forums
    ADD COLUMN topic_count      integer NOT NULL DEFAULT 0,
    ADD COLUMN last_activity_at timestamp with time zone;

ALTER TABLE public.post_reads
    ADD COLUMN last_read_post_id bigint;

-- Backfill forums.topic_count + last_activity_at from current topic state so
-- a freshly-migrated DB matches what new writes will maintain going forward.
UPDATE public.forums f SET
    topic_count = COALESCE(t.n, 0),
    last_activity_at = t.most_recent
FROM (
    SELECT forum_id,
           COUNT(*)::int AS n,
           MAX(last_post_at) AS most_recent
    FROM public.topics
    GROUP BY forum_id
) t
WHERE t.forum_id = f.id;

-- Backfill last_read_post_id by picking the newest post in each topic whose
-- created_at is on-or-before the existing last_read_at marker. Topics with
-- a last_read_at predating every post leave the column NULL (treated as 0
-- in the unread-count query so every post still counts as unread).
UPDATE public.post_reads r SET last_read_post_id = sub.id
FROM (
    SELECT DISTINCT ON (p.topic_id) p.topic_id, p.id, p.created_at
    FROM public.posts p
    ORDER BY p.topic_id, p.created_at DESC, p.id DESC
) sub
WHERE sub.topic_id = r.topic_id
  AND sub.created_at <= r.last_read_at;

-- Existing schema indexes post_reads on topic_id only. The unread aggregation
-- queries filter on user_id, so this index pays for itself the first time a
-- session paints the forum list.
CREATE INDEX IF NOT EXISTS ix_post_reads_user_id ON public.post_reads(user_id);
