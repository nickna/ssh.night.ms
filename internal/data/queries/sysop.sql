-- name: ListUsersAlphabetical :many
-- Sysop console left pane. Bounded to 200 — anything larger needs a real
-- pager UI, which v1 doesn't have.
SELECT id, handle, is_sysop, is_banned, require_ssh_key, last_seen_at, created_at
FROM users
ORDER BY handle ASC
LIMIT 200;

-- name: RecentAuditWithActor :many
-- Sysop console right pane. LEFT JOIN so system actions (actor_id NULL) still
-- surface; COALESCE so sqlc can infer actor_handle as NOT NULL (otherwise it
-- would type the column as a citext value which doesn't scan into a Go
-- string). The sentinel '<system>' renders verbatim in the audit pane.
SELECT a.id, a.actor_id, COALESCE(u.handle, '<system>'::citext)::text AS actor_handle,
       a.action, a.target_type, a.target_id, a.details, a.created_at
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_id
ORDER BY a.created_at DESC
LIMIT 50;

-- name: SetUserBanned :exec
UPDATE users SET is_banned = $2 WHERE id = $1;

-- name: SetUserSysop :exec
UPDATE users SET is_sysop = $2 WHERE id = $1;

-- name: ClearUserRequireSSHKey :exec
-- Recovery hatch: a user who turned on RequireSshKey and then lost their
-- keys can't log in. Only the sysop console hits this — it's not exposed in
-- the user-facing profile.
UPDATE users SET require_ssh_key = FALSE WHERE id = $1;

-- name: InsertAuditLogSimple :exec
-- Same row as InsertAuditLog in audit.sql but with the details column hard-
-- coded to NULL so the sysop screen can fire moderator actions without
-- having to construct a JSON envelope.
INSERT INTO audit_log (actor_id, action, target_type, target_id, details, created_at)
VALUES ($1, $2, $3, $4, NULL, $5);
