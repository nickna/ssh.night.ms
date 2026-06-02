-- security_ip_bans: persistent IP bans.
-- Auto-bans use created_by='auto'; manual sysop bans store the sysop handle.

-- name: UpsertIPBan :exec
INSERT INTO security_ip_bans (ip_addr, banned_at, expires_at, reason, created_by)
VALUES ($1, now(), $2, $3, $4)
ON CONFLICT (ip_addr)
DO UPDATE SET
    banned_at  = EXCLUDED.banned_at,
    expires_at = EXCLUDED.expires_at,
    reason     = EXCLUDED.reason,
    created_by = EXCLUDED.created_by;

-- name: DeleteIPBan :execrows
DELETE FROM security_ip_bans WHERE ip_addr = $1;

-- name: ListActiveIPBans :many
SELECT ip_addr, banned_at, expires_at, reason, created_by
FROM security_ip_bans
WHERE expires_at > now()
ORDER BY banned_at DESC;

-- name: DeleteExpiredIPBans :execrows
DELETE FROM security_ip_bans WHERE expires_at <= now();

-- security_events: append-only audit log of authn / netlimit / handshake events.
-- Used by the sysop UI (events feed) and by external log consumers via JSON.

-- name: InsertSecurityEvent :exec
INSERT INTO security_events (at, event_type, severity, handle, ip_addr, details)
VALUES (now(), $1, $2, $3, $4, $5);

-- name: ListRecentSecurityEvents :many
SELECT id, at, event_type, severity, handle, ip_addr, details
FROM security_events
ORDER BY at DESC
LIMIT $1;

-- name: ListSecurityEventsByType :many
SELECT id, at, event_type, severity, handle, ip_addr, details
FROM security_events
WHERE event_type = $1
ORDER BY at DESC
LIMIT $2;

-- DeleteSecurityEventsBySeverityOlderThan prunes events of one severity tier
-- older than a caller-computed cutoff. The retention sweeper computes the
-- cutoff in Go (now - configured window) so the window is env-driven rather
-- than hardcoded as a SQL interval.

-- name: DeleteSecurityEventsBySeverityOlderThan :execrows
DELETE FROM security_events WHERE severity = $1 AND at < $2;
