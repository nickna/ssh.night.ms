-- OAuth tokens: per-credential access/refresh token storage for linked
-- Google/Microsoft accounts. Keyed 1:1 by identity_credentials.id so SSH
-- credentials (provider='Ssh') simply have no row here. Tokens are stored
-- AEAD-sealed (AES-256-GCM via internal/auth/tokenseal) so a plain DB dump
-- doesn't yield usable access to user Gmail/Drive/Outlook/OneDrive APIs.
--
-- The refresher (internal/auth/oauthrefresh) scans this table for rows
-- whose access_expires_at is approaching now() and refreshes them in
-- batches. On invalid_grant (the user revoked consent or the refresh token
-- expired) it sets needs_reauth=true so the profile UI can surface a
-- "re-authorize" badge; transient 5xx/429/network errors bump
-- refresh_failure_count and only flip needs_reauth=true once the count
-- crosses a threshold so a brief provider outage doesn't disable every
-- linked account.

CREATE TABLE IF NOT EXISTS public.oauth_tokens (
    credential_id           bigint NOT NULL
        CONSTRAINT pk_oauth_tokens PRIMARY KEY
        CONSTRAINT fk_oauth_tokens_identity_credentials
            REFERENCES public.identity_credentials(id) ON DELETE CASCADE,
    encrypted_access_token  bytea NOT NULL,
    encrypted_refresh_token bytea,
    access_expires_at       timestamp with time zone NOT NULL,
    scopes                  text[] NOT NULL DEFAULT '{}',
    token_type              varchar(32) NOT NULL DEFAULT 'Bearer',
    needs_reauth            boolean NOT NULL DEFAULT false,
    last_refreshed_at       timestamp with time zone,
    refresh_failure_count   integer NOT NULL DEFAULT 0,
    created_at              timestamp with time zone NOT NULL,
    updated_at              timestamp with time zone NOT NULL
);

-- Partial index drives the refresher's "soon-expiring, eligible to refresh"
-- scan. Excludes needs_reauth rows (those need user action, not the bot)
-- and rows with no refresh token (nothing to refresh against).
CREATE INDEX IF NOT EXISTS ix_oauth_tokens_expires_active
    ON public.oauth_tokens (access_expires_at)
    WHERE needs_reauth = false AND encrypted_refresh_token IS NOT NULL;
