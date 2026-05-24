-- Sysop-tunable runtime settings. KV table consulted by internal/settings.Cache
-- (in-process snapshot, 30s periodic refresh, push-invalidated across replicas
-- over the Redis channel "system:settings-invalidate"). Defaults for unset
-- keys are layered from config.Options at cache-build time, so unset rows mean
-- "fall back to env-var / compile-time default" — this lets new settings ship
-- without a backfill migration.
--
-- value is text rather than per-type columns; the type column constrains the
-- small set of parsers (bool|int|string|duration_seconds) the Cache knows how
-- to decode. Stringly-typed at the storage edge, strongly-typed at the read
-- edge via settings.Snapshot.

CREATE TABLE public.system_settings (
    key        text NOT NULL CONSTRAINT pk_system_settings PRIMARY KEY,
    value      text NOT NULL,
    type       text NOT NULL CONSTRAINT ck_system_settings_type CHECK (type IN ('bool','int','string','duration_seconds')),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_by bigint NULL
);

ALTER TABLE public.system_settings
    ADD CONSTRAINT fk_system_settings_users_updated_by FOREIGN KEY (updated_by) REFERENCES public.users(id) ON DELETE SET NULL;
