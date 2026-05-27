-- Per-user default landing source for the News screen. Nullable on purpose —
-- a NULL value means "no preference, land on whatever source the registry
-- lists first." The legacy stack ignores unknown columns when reading the
-- row, so this is safe to add without a coordinated legacy migration.

ALTER TABLE public.users ADD COLUMN preferred_news_source TEXT NULL;
