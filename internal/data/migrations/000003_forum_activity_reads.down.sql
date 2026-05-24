DROP INDEX IF EXISTS ix_post_reads_user_id;

ALTER TABLE public.post_reads
    DROP COLUMN IF EXISTS last_read_post_id;

ALTER TABLE public.forums
    DROP COLUMN IF EXISTS last_activity_at,
    DROP COLUMN IF EXISTS topic_count;
