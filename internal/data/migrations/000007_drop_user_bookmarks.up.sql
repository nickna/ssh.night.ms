-- Drops the reader-mode bookmarks table. The Reader screen and the bookmarks
-- provider were removed once the Carbonyl-backed "Web" browser became the
-- only browsing surface — Carbonyl manages its own bookmarks inside the
-- per-user --user-data-dir profile, so the server-side table has no remaining
-- consumer.

DROP TABLE IF EXISTS public.user_bookmarks;
