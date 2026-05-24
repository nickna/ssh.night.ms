-- Sysop-tunable runtime settings. The settings.Cache reads the full table on
-- startup + every 30s + on every "system:settings-invalidate" pub/sub message,
-- so even a coarse-grained ListSystemSettings is plenty fast.

-- name: ListSystemSettings :many
SELECT key, value, type, updated_at, updated_by
FROM system_settings;

-- name: UpsertSystemSetting :exec
INSERT INTO system_settings (key, value, type, updated_at, updated_by)
VALUES ($1, $2, $3, now(), $4)
ON CONFLICT (key)
DO UPDATE SET
    value      = EXCLUDED.value,
    type       = EXCLUDED.type,
    updated_at = EXCLUDED.updated_at,
    updated_by = EXCLUDED.updated_by;

-- name: DeleteSystemSetting :execrows
DELETE FROM system_settings WHERE key = $1;
