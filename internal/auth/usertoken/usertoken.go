// Package usertoken resolves a usable provider access token for a specific
// BBS user on demand. It is the synchronous, interactive-request complement
// to the background oauthrefresh loop: where the refresher proactively renews
// tokens approaching expiry in batches, usertoken handles the "a request just
// arrived and needs a valid token right now" case — including the window
// right after a fresh link (before the first refresher tick) and the case of
// a token that expired between ticks.
//
// It is the single reusable piece every per-user authenticated API client
// (OneNote today; Gmail/Drive/Outlook later) should depend on, rather than
// each re-deriving the decrypt + refresh + reseal dance the unlink path
// open-codes.
//
// Failure modes are typed so callers can render the right user action:
//   - ErrNoLink:       the user never linked this provider.
//   - ErrMissingScope: linked, but the stored grant lacks a required scope
//     (the re-consent gap when scopes are added after a user linked).
//   - ErrNeedsReauth:  the refresh token is dead; the user must re-authorize.
package usertoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/oauthrefresh"
	"github.com/nickna/ssh.night.ms/internal/auth/tokenseal"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// Typed errors. Callers errors.Is against these to choose the user-facing
// remedy (link / re-authorize) instead of surfacing a 500.
var (
	// ErrNoLink: the user has no identity_credentials row for this provider.
	ErrNoLink = errors.New("usertoken: provider not linked")
	// ErrMissingScope: a credential exists but its stored scope set doesn't
	// cover what the caller requires. Happens when a scope is added to the
	// link request after the user already linked — their grant predates it.
	ErrMissingScope = errors.New("usertoken: linked grant missing required scope")
	// ErrNeedsReauth: the token row is flagged needs_reauth, or an on-demand
	// refresh just hard-failed. The user must re-run the link flow.
	ErrNeedsReauth = errors.New("usertoken: re-authorization required")
)

const defaultLeadTime = 2 * time.Minute

// Queries is the subset of the sqlc query set usertoken needs. *gen.Queries
// satisfies it; tests inject a fake. Declared here (rather than taking
// *gen.Queries directly) so the on-demand refresh path is unit-testable
// without a live Postgres.
type Queries interface {
	ListCredentialsForUser(ctx context.Context, userID int64) ([]gen.IdentityCredential, error)
	GetOAuthTokenByCredentialID(ctx context.Context, credentialID int64) (gen.GetOAuthTokenByCredentialIDRow, error)
	UpsertOAuthToken(ctx context.Context, arg gen.UpsertOAuthTokenParams) error
	MarkTokenRefreshFailed(ctx context.Context, arg gen.MarkTokenRefreshFailedParams) error
}

// Source resolves access tokens for one OAuth provider. Construct one per
// provider you call (today only Microsoft). Safe for concurrent use — all
// mutable state lives in Postgres.
type Source struct {
	// Queries is the sqlc query set (or a compatible fake). Required.
	Queries Queries
	// Sealer decrypts the at-rest token blobs. Required.
	Sealer *tokenseal.Sealer
	// Provider is the OAuth provider whose tokens this Source resolves; used
	// for on-demand refresh. Its Kind must match the credential rows this
	// Source reads (e.g. auth.OAuthMicrosoft). Required.
	Provider *auth.OAuthProvider
	// LeadTime is how far before expiry a token is treated as already stale
	// and refreshed pre-emptively, so a request doesn't race the clock.
	// Defaults to 2m.
	LeadTime time.Duration
	// Logger is optional; defaults to slog.Default().
	Logger *slog.Logger
}

func (s *Source) leadTime() time.Duration {
	if s.LeadTime <= 0 {
		return defaultLeadTime
	}
	return s.LeadTime
}

func (s *Source) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Token returns a currently-valid access token for the user, refreshing it on
// demand if it is expired or within LeadTime of expiry. requiredScopes, if
// supplied, must all be present in the stored grant or Token returns
// ErrMissingScope. The provider kind is taken from s.Provider.Kind.
func (s *Source) Token(ctx context.Context, userID int64, requiredScopes ...string) (string, error) {
	if s.Provider == nil {
		return "", errors.New("usertoken: Source.Provider not configured")
	}
	provider := string(s.Provider.Kind)

	credID, err := s.findCredential(ctx, userID, provider)
	if err != nil {
		return "", err
	}

	row, err := s.Queries.GetOAuthTokenByCredentialID(ctx, credID)
	if err != nil {
		return "", fmt.Errorf("usertoken: load token: %w", err)
	}
	if row.NeedsReauth {
		return "", ErrNeedsReauth
	}
	if !hasScopes(row.Scopes, requiredScopes) {
		return "", ErrMissingScope
	}

	// Fresh enough to use directly.
	if row.AccessExpiresAt.Valid && time.Until(row.AccessExpiresAt.Time) > s.leadTime() {
		access, err := s.Sealer.Open(row.EncryptedAccessToken)
		if err != nil {
			// Sealed under a different key / corrupt — the user must re-link.
			return "", ErrNeedsReauth
		}
		return string(access), nil
	}

	// Expired or near-expiry: refresh on demand.
	return s.refresh(ctx, credID, row)
}

// Client returns an *http.Client that injects a valid bearer token on every
// request, transparently using the same on-demand path as Token. base is the
// underlying RoundTripper (pass nil for http.DefaultTransport) — callers
// supply a shared, connection-pooled transport. The token is resolved once at
// call time; long-lived clients that outlive the token's validity should be
// re-created per logical operation (the OneNote service does exactly this).
func (s *Source) Client(ctx context.Context, userID int64, base http.RoundTripper, requiredScopes ...string) (*http.Client, error) {
	tok, err := s.Token(ctx, userID, requiredScopes...)
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport: &bearerTransport{base: base, token: tok},
	}, nil
}

// bearerTransport injects a static bearer token. Cloned-request pattern so we
// never mutate the caller's *http.Request.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// findCredential returns the credential id for the user's link to provider, or
// ErrNoLink. There is at most one row per (user, provider) for OAuth
// providers in practice; if several exist we take the first by created_at
// (the order ListCredentialsForUser already returns).
func (s *Source) findCredential(ctx context.Context, userID int64, provider string) (int64, error) {
	creds, err := s.Queries.ListCredentialsForUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("usertoken: list credentials: %w", err)
	}
	for _, c := range creds {
		if c.Provider == provider {
			return c.ID, nil
		}
	}
	return 0, ErrNoLink
}

// refresh exchanges the stored refresh token for a new access token, re-seals
// both, and persists via the same UpsertOAuthToken statement the background
// refresher uses (its COALESCE handles Microsoft's rotated refresh tokens).
// Hard failures flip needs_reauth and return ErrNeedsReauth, matching the
// refresher's classification via the shared oauthrefresh.IsHardError.
func (s *Source) refresh(ctx context.Context, credID int64, row gen.GetOAuthTokenByCredentialIDRow) (string, error) {
	if len(row.EncryptedRefreshToken) == 0 {
		// No refresh token to renew against — the access token, if any, is
		// effectively dead once expired. Treat as needs-reauth.
		s.markNeedsReauth(ctx, credID)
		return "", ErrNeedsReauth
	}
	refreshPlain, err := s.Sealer.Open(row.EncryptedRefreshToken)
	if err != nil {
		s.markNeedsReauth(ctx, credID)
		return "", ErrNeedsReauth
	}

	tok, err := s.Provider.RefreshToken(ctx, string(refreshPlain))
	if err != nil {
		if oauthrefresh.IsHardError(err) {
			s.markNeedsReauth(ctx, credID)
			return "", ErrNeedsReauth
		}
		// Soft failure (5xx/429/network) — don't flag the row; the background
		// refresher will keep trying. Surface the raw error so the caller can
		// render a transient "try again" rather than a re-auth CTA.
		return "", fmt.Errorf("usertoken: refresh (transient): %w", err)
	}

	if err := s.persist(ctx, credID, tok); err != nil {
		// Persist failed but the token is valid — use it for this request and
		// let the next call re-refresh. Logging, not erroring, keeps the
		// interactive request working.
		s.logger().Warn("usertoken: persist refreshed token", "cred", credID, "err", err)
	}
	return tok.AccessToken, nil
}

func (s *Source) persist(ctx context.Context, credID int64, tok *oauth2.Token) error {
	sealedAccess := s.Sealer.Seal([]byte(tok.AccessToken))
	var sealedRefresh []byte
	if tok.RefreshToken != "" {
		sealedRefresh = s.Sealer.Seal([]byte(tok.RefreshToken))
	}
	now := time.Now().UTC()
	exp := tok.Expiry
	if exp.IsZero() {
		exp = now.Add(time.Hour)
	}
	var scopes []string
	if raw, ok := tok.Extra("scope").(string); ok && raw != "" {
		scopes = strings.Fields(raw)
	}
	tokenType := tok.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return s.Queries.UpsertOAuthToken(ctx, gen.UpsertOAuthTokenParams{
		CredentialID:          credID,
		EncryptedAccessToken:  sealedAccess,
		EncryptedRefreshToken: sealedRefresh,
		AccessExpiresAt:       pgtype.Timestamptz{Time: exp, Valid: true},
		Scopes:                scopes,
		TokenType:             tokenType,
		NeedsReauth:           false,
		LastRefreshedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		RefreshFailureCount:   0,
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	})
}

func (s *Source) markNeedsReauth(ctx context.Context, credID int64) {
	if err := s.Queries.MarkTokenRefreshFailed(ctx, gen.MarkTokenRefreshFailedParams{
		CredentialID: credID,
		NeedsReauth:  true,
		UpdatedAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		s.logger().Warn("usertoken: mark needs_reauth", "cred", credID, "err", err)
	}
}

// hasScopes reports whether every required scope is present in have. Scope
// comparison is case-insensitive because Microsoft echoes scopes back with
// inconsistent casing across the authorize/token/refresh responses. An empty
// required set always passes.
func hasScopes(have, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, s := range have {
		set[strings.ToLower(s)] = struct{}{}
	}
	for _, r := range required {
		if _, ok := set[strings.ToLower(r)]; !ok {
			return false
		}
	}
	return true
}
