-- Unified audit-events queries.
-- Merges the two append-only event streams the sysop screen cares about:
--   audit_log         — sysop-initiated actions (ban/unban/wall/sysop/...)
--   security_events   — authn / netlimit / handshake events from the audit recorder
-- The common projection lets the Events tab render both with one set of types.
-- Static queries here; the dynamic-WHERE filter case lives in
-- internal/data/events_filtered.go (hand-written pgx).

-- name: ListUnifiedEvents :many
-- Paginated chronological tail. `before` is a keyset cursor: pass the
-- timestamp of the last loaded row to fetch the next page. First-page
-- callers pass a NULL Timestamptz (Valid=false) to start from the head.
WITH unified AS (
    SELECT
        'audit'::text AS source,
        a.id,
        a.created_at AS at,
        a.action::text AS kind,
        NULL::text AS severity,
        COALESCE(u.handle, '<system>'::citext)::text AS actor,
        NULL::text AS subject_handle,
        NULL::text AS subject_ip,
        (a.target_type::text || CASE WHEN a.target_id IS NULL THEN '' ELSE '#' || a.target_id::text END)::text AS target,
        a.details
    FROM audit_log a
    LEFT JOIN users u ON u.id = a.actor_id
    UNION ALL
    SELECT
        'security'::text AS source,
        s.id,
        s.at,
        s.event_type AS kind,
        s.severity,
        NULL::text AS actor,
        s.handle AS subject_handle,
        s.ip_addr AS subject_ip,
        NULL::text AS target,
        s.details
    FROM security_events s
)
SELECT source, id, at, kind, severity, actor, subject_handle, subject_ip, target, details
FROM unified
WHERE (CAST(sqlc.narg('before') AS timestamptz) IS NULL OR at < CAST(sqlc.narg('before') AS timestamptz))
ORDER BY at DESC
LIMIT sqlc.arg('row_limit');

-- name: ListUnifiedEventsRelated :many
-- "Related events" pane in the detail modal. Returns events within ±window
-- of `around` that share the same handle or ip. Caller passes either
-- handle or ip (or both) — empty strings disable that branch.
WITH unified AS (
    SELECT
        'audit'::text AS source,
        a.id,
        a.created_at AS at,
        a.action::text AS kind,
        NULL::text AS severity,
        COALESCE(u.handle, '<system>'::citext)::text AS actor,
        NULL::text AS subject_handle,
        NULL::text AS subject_ip,
        (a.target_type::text || CASE WHEN a.target_id IS NULL THEN '' ELSE '#' || a.target_id::text END)::text AS target,
        a.details
    FROM audit_log a
    LEFT JOIN users u ON u.id = a.actor_id
    UNION ALL
    SELECT
        'security'::text AS source,
        s.id,
        s.at,
        s.event_type AS kind,
        s.severity,
        NULL::text AS actor,
        s.handle AS subject_handle,
        s.ip_addr AS subject_ip,
        NULL::text AS target,
        s.details
    FROM security_events s
)
SELECT source, id, at, kind, severity, actor, subject_handle, subject_ip, target, details
FROM unified
WHERE at BETWEEN sqlc.arg('around')::timestamptz - (sqlc.arg('window_seconds')::int * INTERVAL '1 second')
              AND sqlc.arg('around')::timestamptz + (sqlc.arg('window_seconds')::int * INTERVAL '1 second')
  AND (
        (sqlc.arg('match_handle')::text <> '' AND (actor = sqlc.arg('match_handle') OR subject_handle = sqlc.arg('match_handle')))
     OR (sqlc.arg('match_ip')::text <> ''     AND subject_ip = sqlc.arg('match_ip'))
  )
ORDER BY at DESC
LIMIT 20;

-- name: CountUnifiedEvents :one
-- Footer status line ("N total"). Cheaper than counting the CTE — Postgres
-- caches table-level pg_class.reltuples estimates, so two separate counts
-- planned against the per-table indexes beats one count against the CTE
-- materialization. (For exact counts the table totals are still correct;
-- only ambiguous when concurrent writers are mid-transaction.)
SELECT (SELECT count(*) FROM audit_log) + (SELECT count(*) FROM security_events) AS total;
