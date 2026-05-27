// Package oauthrefresh runs the background loop that keeps user-linked
// OAuth access tokens fresh. Without it, every linked Gmail/Drive/Outlook/
// OneDrive credential would silently expire after an hour and the next
// API call would 401 — by which point the user has long since stopped
// thinking about the link.
//
// Lifecycle:
//   - Construct one Refresher at process startup via New().
//   - Launch its Run(ctx) goroutine from main, alongside banCache.Run.
//   - Cancel the context (SIGTERM) to shut down.
//
// Failure semantics:
//   - "Hard" errors (provider returns invalid_grant / invalid_request): the
//     refresh token is dead. Flip needs_reauth=true and stop touching the
//     row until the user re-authorizes via the TUI.
//   - "Soft" errors (provider 5xx/429, network timeout): bump
//     refresh_failure_count without flipping needs_reauth. After ReauthAfter
//     consecutive soft failures (default 5), give up and flip needs_reauth
//     anyway — protects against a long-lived provider outage masquerading
//     as a refresh-loop bug.
//
// Single-replica assumption: there is no claim-locking. If the BBS ever
// scales horizontally, add a claim_until column + UPDATE … RETURNING with
// claim_until < now() filter, or switch the SELECT to FOR UPDATE SKIP
// LOCKED. Both small follow-ups.
package oauthrefresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/tokenseal"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
)

// Defaults tuned for a single-replica BBS with O(100) linked accounts —
// per-tick batch of 50 covers a fully-saturated keyspace within one tick,
// and the 4-worker pool keeps provider HTTP RTTs from serializing.
const (
	defaultInterval   = 60 * time.Second
	defaultLeadTime   = 10 * time.Minute
	defaultWorkers    = 4
	defaultBatchSize  = 50
	defaultReauthAfter = 5 // soft failures before we give up and flip needs_reauth
	perWorkerTimeout  = 20 * time.Second
)

// Config bundles construction params. Most have sensible zero-value
// defaults applied in New so callers only set what they want to override.
type Config struct {
	Queries   *gen.Queries
	Sealer    *tokenseal.Sealer
	Providers map[auth.OAuthProviderKind]*auth.OAuthProvider
	Audit     audit.Recorder
	Logger    *slog.Logger

	Interval    time.Duration
	LeadTime    time.Duration
	Workers     int
	BatchSize   int
	ReauthAfter int
}

// Refresher is the singleton background service. Construct with New() and
// launch Run(ctx). Safe for concurrent use because all state lives in
// Postgres + the per-tick goroutine pool.
type Refresher struct {
	cfg Config
}

// New validates the config and returns a ready Refresher. Returns an error
// only if a required dep (Queries, Sealer, Providers) is missing — the
// time/count knobs all default to sane values.
func New(cfg Config) (*Refresher, error) {
	if cfg.Queries == nil {
		return nil, errors.New("oauthrefresh: Queries required")
	}
	if cfg.Sealer == nil {
		return nil, errors.New("oauthrefresh: Sealer required")
	}
	if cfg.Providers == nil {
		return nil, errors.New("oauthrefresh: Providers required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.LeadTime <= 0 {
		cfg.LeadTime = defaultLeadTime
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.ReauthAfter <= 0 {
		cfg.ReauthAfter = defaultReauthAfter
	}
	return &Refresher{cfg: cfg}, nil
}

// Run is the main loop. Returns when ctx is cancelled. Errors during a
// tick are logged and don't abort the loop — we want this thing to keep
// trying through transient DB/provider blips.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.tick(ctx); err != nil {
				r.cfg.Logger.Warn("oauthrefresh tick", "err", err)
			}
		}
	}
}

func (r *Refresher) tick(ctx context.Context) error {
	rows, err := r.cfg.Queries.ListExpiringTokens(ctx, gen.ListExpiringTokensParams{
		LeadTime:  pgtype.Interval{Microseconds: r.cfg.LeadTime.Microseconds(), Valid: true},
		BatchSize: int32(r.cfg.BatchSize),
	})
	if err != nil {
		return fmt.Errorf("list expiring: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	jobs := make(chan gen.ListExpiringTokensRow, len(rows))
	for _, row := range rows {
		jobs <- row
	}
	close(jobs)
	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range jobs {
				r.refreshOne(ctx, row)
			}
		}()
	}
	wg.Wait()
	return nil
}

func (r *Refresher) refreshOne(ctx context.Context, row gen.ListExpiringTokensRow) {
	provider := r.cfg.Providers[auth.OAuthProviderKind(row.Provider)]
	if provider == nil {
		// Configuration removed under our feet — skip this row silently.
		// The next link attempt for the provider will surface "unavailable"
		// to the user; flipping needs_reauth here would be misleading.
		return
	}

	refreshTok, err := r.cfg.Sealer.Open(row.EncryptedRefreshToken)
	if err != nil {
		// Sealed-blob corrupt or sealed under a different key — user must
		// re-authorize. Hard fail.
		r.markHardFail(ctx, row, "sealer", err.Error())
		return
	}

	tctx, cancel := context.WithTimeout(ctx, perWorkerTimeout)
	defer cancel()
	tok, err := provider.RefreshToken(tctx, string(refreshTok))
	if err != nil {
		if isHardOAuthError(err) {
			r.markHardFail(ctx, row, oauthErrorCode(err), err.Error())
			return
		}
		// Soft fail — bump counter. If we've crossed the threshold, also
		// flip needs_reauth as a safety valve against indefinite outages.
		needsReauth := row.RefreshFailureCount+1 >= int32(r.cfg.ReauthAfter)
		if updErr := r.cfg.Queries.MarkTokenRefreshFailed(ctx, gen.MarkTokenRefreshFailedParams{
			CredentialID: row.CredentialID,
			NeedsReauth:  needsReauth,
			UpdatedAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		}); updErr != nil {
			r.cfg.Logger.Warn("oauthrefresh: mark fail", "cred", row.CredentialID, "err", updErr)
		}
		r.emitSoftFail(ctx, row, err, int(row.RefreshFailureCount)+1)
		if needsReauth {
			r.emitReauthRequired(ctx, row, "soft-failure threshold crossed")
		}
		return
	}

	// Success: re-seal and upsert. UpsertOAuthToken's COALESCE on
	// encrypted_refresh_token handles both rotated (Microsoft) and
	// preserved (Google usually) refresh tokens correctly.
	if err := r.persistRefreshed(ctx, row, tok); err != nil {
		r.cfg.Logger.Warn("oauthrefresh: persist", "cred", row.CredentialID, "err", err)
		return
	}
	r.emitRefreshed(ctx, row, tok)
}

func (r *Refresher) persistRefreshed(ctx context.Context, row gen.ListExpiringTokensRow, tok *oauth2.Token) error {
	sealedAccess := r.cfg.Sealer.Seal([]byte(tok.AccessToken))
	var sealedRefresh []byte
	if tok.RefreshToken != "" {
		sealedRefresh = r.cfg.Sealer.Seal([]byte(tok.RefreshToken))
	}
	now := time.Now().UTC()
	exp := tok.Expiry
	if exp.IsZero() {
		exp = now.Add(time.Hour)
	}
	var scopes []string
	if raw, ok := tok.Extra("scope").(string); ok && raw != "" {
		scopes = splitScopes(raw)
	}
	tokenType := tok.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return r.cfg.Queries.UpsertOAuthToken(ctx, gen.UpsertOAuthTokenParams{
		CredentialID:          row.CredentialID,
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

func (r *Refresher) markHardFail(ctx context.Context, row gen.ListExpiringTokensRow, code, msg string) {
	if err := r.cfg.Queries.MarkTokenRefreshFailed(ctx, gen.MarkTokenRefreshFailedParams{
		CredentialID: row.CredentialID,
		NeedsReauth:  true,
		UpdatedAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		r.cfg.Logger.Warn("oauthrefresh: mark hard fail", "cred", row.CredentialID, "err", err)
	}
	r.cfg.Logger.Warn("oauthrefresh: hard fail",
		"cred", row.CredentialID, "provider", row.Provider, "code", code, "err", msg)
	r.emitReauthRequired(ctx, row, code+": "+msg)
}

func (r *Refresher) emitRefreshed(ctx context.Context, row gen.ListExpiringTokensRow, tok *oauth2.Token) {
	if r.cfg.Audit == nil {
		return
	}
	prev := time.Time{}
	if row.AccessExpiresAt.Valid {
		prev = row.AccessExpiresAt.Time
	}
	r.cfg.Audit.Record(ctx, audit.OAuthRefreshed{
		Provider:        row.Provider,
		ProviderSubject: row.Subject,
		PrevExpiresAt:   prev,
		NewExpiresAt:    tok.Expiry,
	})
}

func (r *Refresher) emitSoftFail(ctx context.Context, row gen.ListExpiringTokensRow, err error, attempt int) {
	if r.cfg.Audit == nil {
		return
	}
	r.cfg.Audit.Record(ctx, audit.OAuthRefreshFailed{
		Provider:        row.Provider,
		ProviderSubject: row.Subject,
		ErrCode:         oauthErrorCode(err),
		ErrMsg:          err.Error(),
		AttemptCount:    attempt,
	})
}

func (r *Refresher) emitReauthRequired(ctx context.Context, row gen.ListExpiringTokensRow, reason string) {
	if r.cfg.Audit == nil {
		return
	}
	r.cfg.Audit.Record(ctx, audit.OAuthReauthRequired{
		Provider:        row.Provider,
		ProviderSubject: row.Subject,
		Reason:          reason,
	})
}

// isHardOAuthError returns true when the provider's response indicates the
// refresh token itself is no longer usable (invalid_grant being the canonical
// signal: the user revoked consent, the refresh token expired, the app was
// removed from the user's authorized list). Soft errors (network, 5xx, 429)
// are retried.
func isHardOAuthError(err error) bool {
	var rerr *oauth2.RetrieveError
	if !errors.As(err, &rerr) {
		return false
	}
	switch rerr.ErrorCode {
	case "invalid_grant", "invalid_request", "invalid_client", "unauthorized_client":
		return true
	}
	// Treat 400-class status codes without a recognized error code as hard
	// failures too — a 400 from the token endpoint without an error code
	// means we sent something malformed and retrying won't help.
	if rerr.Response != nil && rerr.Response.StatusCode >= 400 && rerr.Response.StatusCode < 500 && rerr.ErrorCode == "" {
		return true
	}
	return false
}

func oauthErrorCode(err error) string {
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) {
		if rerr.ErrorCode != "" {
			return rerr.ErrorCode
		}
		if rerr.Response != nil {
			return fmt.Sprintf("status_%d", rerr.Response.StatusCode)
		}
	}
	return "unknown"
}

// splitScopes splits the space-separated scope string the IdP returns into
// the text[] form Postgres wants.
func splitScopes(raw string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == ' ' {
			if i > start {
				out = append(out, raw[start:i])
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}
