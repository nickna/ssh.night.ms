-- name: GetCredentialByProviderSubject :one
-- Provider is stored as the enum name string ("Ssh", "Google", "Microsoft").
SELECT *
FROM identity_credentials
WHERE provider = $1
  AND subject = $2;

-- name: TouchCredentialLastUsed :exec
UPDATE identity_credentials
SET last_used_at = $2
WHERE id = $1;

-- name: ListCredentialsForUser :many
SELECT *
FROM identity_credentials
WHERE user_id = $1
ORDER BY created_at;

-- name: ListSshCredentialsForUser :many
-- Provider is "Ssh"; newest last so the profile UI can show "added <date>"
-- in a sensible order.
SELECT id, user_id, provider, subject, metadata, label, created_at, last_used_at
FROM identity_credentials
WHERE user_id = $1 AND provider = 'Ssh'
ORDER BY created_at;

-- name: InsertSshCredential :one
-- Inserts an SSH public-key credential. (provider, subject) is uniquely
-- indexed so a duplicate fingerprint (even under a different label) is
-- rejected by Postgres — the caller surfaces that as a friendly notice.
INSERT INTO identity_credentials (user_id, provider, subject, metadata, label, created_at)
VALUES ($1, 'Ssh', $2, $3, $4, $5)
RETURNING id, user_id, provider, subject, metadata, label, created_at, last_used_at;

-- name: InsertOAuthCredential :one
-- Inserts an OAuth credential. Generic — provider is "Google", "Microsoft",
-- etc. subject is the provider's stable per-user identifier (sub claim for
-- Google, oid for Microsoft).
INSERT INTO identity_credentials (user_id, provider, subject, metadata, label, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, user_id, provider, subject, metadata, label, created_at, last_used_at;

-- name: DeleteCredentialByID :execrows
-- Owner-guarded: refuses to delete somebody else's credential even if the
-- caller passes a stale id. :execrows so the handler can detect a no-op.
DELETE FROM identity_credentials
WHERE id = $1 AND user_id = $2;

-- name: CountSshCredentialsForUser :one
-- Used by the profile screen's lockout guard before enabling "Require SSH key"
-- and before deleting a key in passwordless mode. Cast to int so sqlc emits
-- a fixed-width int64 instead of pgtype numeric.
SELECT COUNT(*)::bigint AS count
FROM identity_credentials
WHERE user_id = $1 AND provider = 'Ssh';
