-- name: GetCredentialByProviderSubject :one
-- Provider is stored as the enum name string ("Ssh", "Google", "Microsoft").
SELECT *
FROM identity_credentials
WHERE provider = $1
  AND subject = $2;

-- name: GetCredentialByID :one
-- Look up a credential by primary key. Used by the unlink path to read
-- provider + subject for the audit event and the best-effort RevokeToken
-- call before the DELETE wipes the row.
SELECT *
FROM identity_credentials
WHERE id = $1;

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

-- name: DeleteAllSshCredentialsForUser :execrows
-- Sysop "remove SSH keys" action — wipes every SSH key for a target user
-- in one statement. :execrows so the handler can report how many were
-- removed.
DELETE FROM identity_credentials
WHERE user_id = $1 AND provider = 'Ssh';

-- name: CountSshCredentialsForUser :one
-- Used by the profile screen's lockout guard before enabling "Require SSH key"
-- and before deleting a key in passwordless mode. Cast to int so sqlc emits
-- a fixed-width int64 instead of pgtype numeric.
SELECT COUNT(*)::bigint AS count
FROM identity_credentials
WHERE user_id = $1 AND provider = 'Ssh';

-- name: ListOAuthCredentialsForUser :many
-- Joined identity_credentials × oauth_tokens view for the linked-accounts
-- UI. LEFT JOIN so a credential row without (yet) a token row still
-- surfaces — that state shouldn't happen post-Phase 2 but the join is
-- defensive against partial inserts.
SELECT
    c.id, c.provider, c.subject, c.metadata, c.label,
    c.created_at, c.last_used_at,
    t.access_expires_at, t.needs_reauth, t.last_refreshed_at,
    t.scopes, t.refresh_failure_count
FROM identity_credentials c
LEFT JOIN oauth_tokens t ON t.credential_id = c.id
WHERE c.user_id = $1
  AND c.provider IN ('Google', 'Microsoft')
ORDER BY c.created_at;

-- name: InsertOAuthToken :exec
-- Used by the initial-link path inside the same transaction as
-- InsertOAuthCredential. encrypted_refresh_token may be NULL when the
-- provider didn't return one (Google omits it without prompt=consent;
-- Microsoft always returns one with offline_access).
INSERT INTO oauth_tokens (
    credential_id, encrypted_access_token, encrypted_refresh_token,
    access_expires_at, scopes, token_type,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $7
);

-- name: UpsertOAuthToken :exec
-- Used by both the re-auth path (TUI device-code or browser callback on an
-- existing credential) and by the refresher loop. The COALESCE on
-- encrypted_refresh_token preserves the prior refresh token when the
-- provider didn't rotate it on this exchange — both Google ("usually
-- doesn't rotate") and Microsoft ("always rotates") are handled correctly.
INSERT INTO oauth_tokens (
    credential_id, encrypted_access_token, encrypted_refresh_token,
    access_expires_at, scopes, token_type,
    needs_reauth, last_refreshed_at, refresh_failure_count,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10
)
ON CONFLICT (credential_id) DO UPDATE SET
    encrypted_access_token  = EXCLUDED.encrypted_access_token,
    encrypted_refresh_token = COALESCE(EXCLUDED.encrypted_refresh_token, oauth_tokens.encrypted_refresh_token),
    access_expires_at       = EXCLUDED.access_expires_at,
    scopes                  = EXCLUDED.scopes,
    token_type              = EXCLUDED.token_type,
    needs_reauth            = EXCLUDED.needs_reauth,
    last_refreshed_at       = EXCLUDED.last_refreshed_at,
    refresh_failure_count   = EXCLUDED.refresh_failure_count,
    updated_at              = EXCLUDED.updated_at;

-- name: GetOAuthTokenByCredentialID :one
-- Fetches the sealed token row for a credential. Used by:
--   - the refresher (to decrypt + refresh a single row)
--   - the unlink path (to revoke at the provider before delete)
--   - future Gmail/Drive/etc clients
SELECT credential_id, encrypted_access_token, encrypted_refresh_token,
       access_expires_at, scopes, token_type,
       needs_reauth, last_refreshed_at, refresh_failure_count
FROM oauth_tokens
WHERE credential_id = $1;

-- name: ListExpiringTokens :many
-- Drives the refresher's per-tick scan. Returns rows that will expire
-- within $1 from now and are still eligible (not needs_reauth, has a
-- refresh token). Includes the encrypted refresh token + provider so the
-- worker can decrypt + call the right RefreshToken endpoint. LIMIT caps
-- the batch so a backlog can't stall the loop.
SELECT
    c.id           AS credential_id,
    c.provider     AS provider,
    c.subject      AS subject,
    t.encrypted_refresh_token,
    t.access_expires_at,
    t.refresh_failure_count
FROM oauth_tokens t
JOIN identity_credentials c ON c.id = t.credential_id
WHERE t.needs_reauth = false
  AND t.encrypted_refresh_token IS NOT NULL
  AND t.access_expires_at < (now() + sqlc.arg(lead_time)::interval)
ORDER BY t.access_expires_at ASC
LIMIT sqlc.arg(batch_size);

-- name: MarkTokenRefreshFailed :exec
-- Soft-failure path: bump the failure counter; the caller decides whether
-- to also flip needs_reauth (caller passes the desired state in $2).
UPDATE oauth_tokens
SET refresh_failure_count = refresh_failure_count + 1,
    needs_reauth          = $2,
    updated_at            = $3
WHERE credential_id = $1;

-- name: OAuthTokenStats :many
-- Periodic stats query feeding the expvar gauges. Grouped by provider so
-- the dashboard can split "Google active / needs reauth" from Microsoft.
SELECT
    c.provider,
    COUNT(*) FILTER (WHERE t.needs_reauth = false)::bigint AS active,
    COUNT(*) FILTER (WHERE t.needs_reauth = true)::bigint  AS needs_reauth
FROM oauth_tokens t
JOIN identity_credentials c ON c.id = t.credential_id
GROUP BY c.provider;
