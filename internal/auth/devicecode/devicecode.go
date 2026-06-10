// Package devicecode runs the OAuth 2.0 device authorization grant
// (RFC 8628) for linking Google + Microsoft accounts from the SSH TUI.
//
// The terminal can't host a browser, so the device-code flow shows the
// user a short alphanumeric code + a verification URL — the user opens the
// URL in any browser, enters the code, approves consent, and the TUI
// poll-loop catches the resulting Approved response. The flow state lives
// in Redis (TTL = code expiry, normally 5–15 minutes) so process restarts
// don't strand in-progress users for long.
//
// Operator setup: Google's device flow REJECTS regular "Web application"
// OAuth clients with error=disabled_client. A separate client of type
// "TVs and Limited Input devices" must be registered in Google Cloud
// Console — its client_id + secret feed the Google branch of this
// package. Microsoft accepts the same client ID for both browser and
// device flows as long as "Allow public client flows" is enabled on the
// Azure app registration.
//
// Tokens received via this flow are sealed and inserted into oauth_tokens
// in the same transaction as the identity_credentials row, so a partial
// failure leaves no orphaned credentials.
package devicecode

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/auth/tokenseal"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
)

// Redis key prefixes. All keys live under "oauth:device:" so a wildcard
// scan from the sysop console can survey active flows without sweeping the
// whole keyspace.
const (
	keyFlowPrefix  = "oauth:device:flow:"  // + flow_id  → JSON state, TTL = expires_in
	keyUserPrefix  = "oauth:device:user:"  // + user_id:provider → flow_id, TTL = expires_in
	keyBeginBucket = "oauth:device:begin:" // + user_id → INCR counter, EX 60s
	beginRateLimit = 6                     // starts per minute per user
	beginRateTTL   = 60 * time.Second
)

// ErrProviderUnavailable is returned by Begin when the device-code OAuth
// client isn't configured for the requested provider. The TUI surfaces it
// as "linking from terminal is unavailable — link via the web profile
// page". Distinct from a network error so the UI can phrase appropriately.
var ErrProviderUnavailable = errors.New("devicecode: provider not configured for device flow")

// ErrRateLimited is returned by Begin when the per-user rate bucket is
// exhausted.
var ErrRateLimited = errors.New("devicecode: too many recent attempts")

// Flow is what we hand back to the TUI on Begin — the user-facing fields
// (UserCode, VerificationURL) plus the opaque server-side handle the TUI
// uses on subsequent Poll calls.
type Flow struct {
	ID              string // opaque server-minted handle; client passes back to Poll
	Provider        auth.OAuthProviderKind
	UserCode        string        // short code the user types into the verification page
	VerificationURL string        // URL the user opens in a browser
	Interval        time.Duration // minimum gap between Poll calls (provider-specified)
	ExpiresAt       time.Time     // hard deadline; after this Poll returns ResultExpired
}

// flowState is the JSON blob persisted to Redis. Mirrors Flow + adds the
// server-only DeviceCode (the secret poll token; never leaked to the
// client) and the polling bookkeeping fields.
type flowState struct {
	UserID          int64                  `json:"user_id"`
	Provider        auth.OAuthProviderKind `json:"provider"`
	DeviceCode      string                 `json:"device_code"`
	UserCode        string                 `json:"user_code"`
	VerificationURL string                 `json:"verification_url"`
	IntervalSeconds int                    `json:"interval_s"`
	ExpiresAt       time.Time              `json:"expires_at"`
	LastPollAt      time.Time              `json:"last_poll_at,omitempty"`
}

// ResultKind enumerates the outcomes of a Poll call. The TUI state machine
// branches on this directly — no nested error types.
type ResultKind int

const (
	// ResultPending: the user hasn't approved yet. UI shows "waiting…",
	// schedules the next tick at NextPollAfter.
	ResultPending ResultKind = iota
	// ResultSlowDown: the IdP is asking us to back off. UI bumps the
	// interval (NextPollAfter is already adjusted).
	ResultSlowDown
	// ResultApproved: tokens received, credential row created/upserted.
	// Credential is populated.
	ResultApproved
	// ResultDenied: the user clicked "Cancel" on the consent screen.
	ResultDenied
	// ResultExpired: the device code TTL has elapsed without approval.
	ResultExpired
	// ResultDuplicate: the OAuth account is already linked to a DIFFERENT
	// SSH user. UI surfaces "this {provider} account is linked to a
	// different handle".
	ResultDuplicate
)

// Result is what Poll returns. NextPollAfter is the suggested delay before
// the next Poll (only meaningful for Pending/SlowDown). Credential is set
// on Approved.
type Result struct {
	Kind          ResultKind
	NextPollAfter time.Duration
	Credential    *gen.IdentityCredential
}

// Service drives the device flow. Construct once at process startup with
// the providers wired by main; safe for concurrent use.
type Service struct {
	Pool      pgPool
	Queries   *gen.Queries
	Redis     *redis.Client
	Sealer    *tokenseal.Sealer
	Providers map[auth.OAuthProviderKind]*auth.OAuthProvider
	Audit     audit.Recorder
	Logger    *slog.Logger
	HTTP      *http.Client
	Now       func() time.Time // injectable for tests
}

// pgPool is the narrow subset of *pgxpool.Pool we need — keeps unit tests
// from having to spin up a real pool. Production wires *pgxpool.Pool.
type pgPool interface {
	Begin(ctx context.Context) (pgxTx, error)
}

// pgxTx is the narrow tx interface — only Commit + Rollback, plus the
// ability to wrap Queries. The implementation is pgxPoolAdapter below.
type pgxTx interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	Queries() *gen.Queries
}

// Begin starts a device-code flow for the given user + provider. Returns
// the user-visible Flow on success; the TUI displays UserCode +
// VerificationURL and starts polling on Interval.
func (s *Service) Begin(ctx context.Context, userID int64, provider auth.OAuthProviderKind) (*Flow, error) {
	p := s.Providers[provider]
	if p == nil {
		return nil, ErrProviderUnavailable
	}
	if err := s.enforceBeginRate(ctx, userID); err != nil {
		return nil, err
	}

	// Drop any pre-existing flow for this user/provider — second Begin
	// silently supersedes the first. Keeps the "one active flow per user"
	// invariant simple.
	if oldID, err := s.Redis.Get(ctx, userKey(userID, provider)).Result(); err == nil && oldID != "" {
		_, _ = s.Redis.Del(ctx, flowKey(oldID), userKey(userID, provider)).Result()
	}

	resp, err := s.requestDeviceCode(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("devicecode: request: %w", err)
	}

	flowID, err := newFlowID()
	if err != nil {
		return nil, fmt.Errorf("devicecode: mint id: %w", err)
	}
	now := s.now()
	expires := now.Add(time.Duration(resp.ExpiresIn) * time.Second)
	state := flowState{
		UserID:          userID,
		Provider:        provider,
		DeviceCode:      resp.DeviceCode,
		UserCode:        resp.UserCode,
		VerificationURL: resp.VerificationURL,
		IntervalSeconds: resp.Interval,
		ExpiresAt:       expires,
	}
	payload, _ := json.Marshal(state)
	ttl := time.Duration(resp.ExpiresIn) * time.Second
	if err := s.Redis.Set(ctx, flowKey(flowID), payload, ttl).Err(); err != nil {
		return nil, fmt.Errorf("devicecode: persist flow: %w", err)
	}
	if err := s.Redis.Set(ctx, userKey(userID, provider), flowID, ttl).Err(); err != nil {
		// Best-effort cleanup of the orphaned flow row before bailing.
		_, _ = s.Redis.Del(ctx, flowKey(flowID)).Result()
		return nil, fmt.Errorf("devicecode: persist user mapping: %w", err)
	}

	return &Flow{
		ID:              flowID,
		Provider:        provider,
		UserCode:        resp.UserCode,
		VerificationURL: resp.VerificationURL,
		Interval:        time.Duration(resp.Interval) * time.Second,
		ExpiresAt:       expires,
	}, nil
}

// Poll exchanges the device_code for a token. The IdP returns one of:
// authorization_pending (user hasn't acted), slow_down (we're polling too
// fast), expired_token (device code TTL elapsed), access_denied (user
// clicked cancel), or a success body with access_token + refresh_token.
// On Approved, the credential + token rows are inserted/upserted in one
// transaction.
func (s *Service) Poll(ctx context.Context, flowID string) (Result, error) {
	raw, err := s.Redis.Get(ctx, flowKey(flowID)).Result()
	if errors.Is(err, redis.Nil) {
		return Result{Kind: ResultExpired}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("devicecode: load flow: %w", err)
	}
	var state flowState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return Result{}, fmt.Errorf("devicecode: decode flow: %w", err)
	}

	p := s.Providers[state.Provider]
	if p == nil {
		// Configuration changed mid-flow — bail with Expired rather than
		// confusing the user.
		s.cleanupFlow(ctx, flowID, state)
		return Result{Kind: ResultExpired}, nil
	}

	if s.now().After(state.ExpiresAt) {
		s.cleanupFlow(ctx, flowID, state)
		return Result{Kind: ResultExpired}, nil
	}

	interval := time.Duration(state.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if remain := state.LastPollAt.Add(interval).Sub(s.now()); !state.LastPollAt.IsZero() && remain > 0 {
		return Result{Kind: ResultPending, NextPollAfter: remain}, nil
	}

	tok, errCode, err := s.exchangeDeviceCode(ctx, p, state.DeviceCode)
	if err != nil {
		return Result{}, fmt.Errorf("devicecode: exchange: %w", err)
	}
	state.LastPollAt = s.now()
	switch errCode {
	case "":
		// Success — token populated below.
	case "authorization_pending":
		s.persistFlow(ctx, flowID, state)
		return Result{Kind: ResultPending, NextPollAfter: interval}, nil
	case "slow_down":
		// IdP wants ≥5s longer. Bump our cached interval.
		state.IntervalSeconds += 5
		s.persistFlow(ctx, flowID, state)
		return Result{Kind: ResultSlowDown, NextPollAfter: time.Duration(state.IntervalSeconds) * time.Second}, nil
	case "access_denied":
		s.cleanupFlow(ctx, flowID, state)
		return Result{Kind: ResultDenied}, nil
	case "expired_token":
		s.cleanupFlow(ctx, flowID, state)
		return Result{Kind: ResultExpired}, nil
	default:
		// Unknown error code from provider; surface as Denied so the user
		// can restart, and log for triage.
		s.Logger.Warn("devicecode: unknown error from provider", "code", errCode, "provider", state.Provider)
		s.cleanupFlow(ctx, flowID, state)
		return Result{Kind: ResultDenied}, nil
	}

	// Approved path: fetch userinfo + commit credential + tokens.
	user, err := p.FetchUserInfo(ctx, tok)
	if err != nil {
		return Result{}, fmt.Errorf("devicecode: userinfo: %w", err)
	}
	cred, kind, err := s.commitApproved(ctx, state.UserID, state.Provider, user, tok)
	if err != nil {
		return Result{}, err
	}
	if kind == ResultDuplicate {
		// Don't delete the flow on duplicate — it cost the user a real
		// approval. But the flow is now resolved; clean up Redis state.
		s.cleanupFlow(ctx, flowID, state)
		return Result{Kind: ResultDuplicate}, nil
	}
	s.cleanupFlow(ctx, flowID, state)
	s.emitLinkAudit(ctx, state.UserID, state.Provider, user.Subject)
	return Result{Kind: ResultApproved, Credential: cred}, nil
}

// commitApproved resolves the (provider, subject) lookup → routes to the
// right write. Returns either ResultApproved (link or re-auth) with the
// credential row, or ResultDuplicate when the OAuth identity already
// belongs to a different user. Errors are infrastructure failures only.
func (s *Service) commitApproved(
	ctx context.Context,
	userID int64,
	provider auth.OAuthProviderKind,
	user auth.OAuthUser,
	tok *oauth2.Token,
) (*gen.IdentityCredential, ResultKind, error) {
	lookup, existing, err := auth.ResolveExistingLink(ctx, s.Queries, userID, provider, user.Subject)
	if err != nil {
		return nil, 0, fmt.Errorf("devicecode: resolve existing: %w", err)
	}
	switch lookup {
	case auth.LinkLookupOtherUser:
		return nil, ResultDuplicate, nil
	case auth.LinkLookupSameUser:
		// Re-auth path. Upsert the token row in place; credential row is
		// reused. No tx needed — single statement.
		if err := s.upsertTokens(ctx, s.Queries, existing.ID, tok); err != nil {
			return nil, 0, err
		}
		return existing, ResultApproved, nil
	}

	// Fresh link path: insert credential + token in one tx so either both
	// land or neither.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("devicecode: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	q := tx.Queries()

	metadata, _ := json.Marshal(map[string]any{"email": user.Email, "name": user.Name})
	label := "linked via device-code"
	now := s.now()
	cred, err := q.InsertOAuthCredential(ctx, gen.InsertOAuthCredentialParams{
		UserID:    userID,
		Provider:  string(provider),
		Subject:   user.Subject,
		Metadata:  metadata,
		Label:     &label,
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("devicecode: insert credential: %w", err)
	}

	if err := s.insertTokens(ctx, q, cred.ID, tok); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fmt.Errorf("devicecode: commit: %w", err)
	}
	return &cred, ResultApproved, nil
}

// upsertTokens writes the token row for an existing credential. Used by
// the re-auth path AND by the refresher (separately).
func (s *Service) upsertTokens(ctx context.Context, q *gen.Queries, credID int64, tok *oauth2.Token) error {
	sealedAccess := s.Sealer.Seal([]byte(tok.AccessToken))
	var sealedRefresh []byte
	if tok.RefreshToken != "" {
		sealedRefresh = s.Sealer.Seal([]byte(tok.RefreshToken))
	}
	now := s.now()
	exp := tok.Expiry
	if exp.IsZero() {
		exp = now.Add(time.Hour) // safe default; both providers issue ~1h tokens
	}
	return q.UpsertOAuthToken(ctx, gen.UpsertOAuthTokenParams{
		CredentialID:          credID,
		EncryptedAccessToken:  sealedAccess,
		EncryptedRefreshToken: sealedRefresh,
		AccessExpiresAt:       pgtype.Timestamptz{Time: exp, Valid: true},
		Scopes:                tokenScopes(tok),
		TokenType:             defaultTokenType(tok.TokenType),
		NeedsReauth:           false,
		LastRefreshedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		RefreshFailureCount:   0,
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	})
}

// insertTokens writes the initial token row for a freshly-linked
// credential (called inside the same tx as InsertOAuthCredential).
func (s *Service) insertTokens(ctx context.Context, q *gen.Queries, credID int64, tok *oauth2.Token) error {
	sealedAccess := s.Sealer.Seal([]byte(tok.AccessToken))
	var sealedRefresh []byte
	if tok.RefreshToken != "" {
		sealedRefresh = s.Sealer.Seal([]byte(tok.RefreshToken))
	}
	now := s.now()
	exp := tok.Expiry
	if exp.IsZero() {
		exp = now.Add(time.Hour)
	}
	return q.InsertOAuthToken(ctx, gen.InsertOAuthTokenParams{
		CredentialID:          credID,
		EncryptedAccessToken:  sealedAccess,
		EncryptedRefreshToken: sealedRefresh,
		AccessExpiresAt:       pgtype.Timestamptz{Time: exp, Valid: true},
		Scopes:                tokenScopes(tok),
		TokenType:             defaultTokenType(tok.TokenType),
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	})
}

// tokenScopes extracts the "scope" extra param from the token response and
// splits it. Providers return scopes as a single space-separated string;
// callers want a slice for the text[] column.
func tokenScopes(tok *oauth2.Token) []string {
	if tok == nil {
		return nil
	}
	if raw, ok := tok.Extra("scope").(string); ok && raw != "" {
		return strings.Fields(raw)
	}
	return nil
}

func defaultTokenType(t string) string {
	if t == "" {
		return "Bearer"
	}
	return t
}

func (s *Service) emitLinkAudit(ctx context.Context, userID int64, provider auth.OAuthProviderKind, subject string) {
	if s.Audit == nil {
		return
	}
	user, err := s.Queries.GetUserByID(ctx, userID)
	handle := ""
	if err == nil {
		handle = user.Handle
	}
	s.Audit.Record(ctx, audit.OAuthLinked{
		Handle:          handle,
		Provider:        string(provider),
		ProviderSubject: subject,
		Method:          "device",
	})
}

func (s *Service) enforceBeginRate(ctx context.Context, userID int64) error {
	key := keyBeginBucket + strconv.FormatInt(userID, 10)
	count, err := s.Redis.Incr(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("devicecode: rate counter: %w", err)
	}
	if count == 1 {
		// First INCR — set the expiry so the bucket actually resets.
		_, _ = s.Redis.Expire(ctx, key, beginRateTTL).Result()
	}
	if count > beginRateLimit {
		return ErrRateLimited
	}
	return nil
}

func (s *Service) cleanupFlow(ctx context.Context, flowID string, state flowState) {
	_, _ = s.Redis.Del(ctx, flowKey(flowID), userKey(state.UserID, state.Provider)).Result()
}

func (s *Service) persistFlow(ctx context.Context, flowID string, state flowState) {
	payload, _ := json.Marshal(state)
	ttl := state.ExpiresAt.Sub(s.now())
	if ttl <= 0 {
		s.cleanupFlow(ctx, flowID, state)
		return
	}
	_, _ = s.Redis.Set(ctx, flowKey(flowID), payload, ttl).Result()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) httpClient() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// deviceCodeResp is the normalized device-code endpoint response. Both
// Google and Microsoft return roughly the same shape; we unmarshal into
// this struct using `omitempty` because Google returns verification_url
// and Microsoft returns verification_uri.
type deviceCodeResp struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string // computed: prefer verification_uri (RFC 8628), fall back to verification_url
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type rawDeviceCodeResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	VerificationURL         string `json:"verification_url"` // Google's older field name
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (s *Service) requestDeviceCode(ctx context.Context, p *auth.OAuthProvider) (*deviceCodeResp, error) {
	endpoint, err := deviceCodeEndpoint(p.Kind)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("client_id", p.Config.ClientID)
	form.Set("scope", strings.Join(p.Config.Scopes, " "))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code endpoint status %d: %s", resp.StatusCode, string(body))
	}
	var raw rawDeviceCodeResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode device code: %w", err)
	}
	if raw.DeviceCode == "" || raw.UserCode == "" {
		return nil, fmt.Errorf("device code endpoint returned empty fields")
	}
	verURL := raw.VerificationURI
	if verURL == "" {
		verURL = raw.VerificationURL
	}
	if verURL == "" {
		return nil, fmt.Errorf("device code endpoint returned no verification url")
	}
	if raw.Interval == 0 {
		raw.Interval = 5 // RFC 8628 default
	}
	return &deviceCodeResp{
		DeviceCode:      raw.DeviceCode,
		UserCode:        raw.UserCode,
		VerificationURL: verURL,
		ExpiresIn:       raw.ExpiresIn,
		Interval:        raw.Interval,
	}, nil
}

// exchangeDeviceCode POSTs to the token endpoint with grant_type=device_code.
// On success returns (*oauth2.Token, "", nil). On a defined error code
// (authorization_pending, slow_down, etc.) returns (nil, code, nil) — only
// infrastructure failures return a non-nil error.
func (s *Service) exchangeDeviceCode(ctx context.Context, p *auth.OAuthProvider, deviceCode string) (*oauth2.Token, string, error) {
	endpoint, err := tokenEndpoint(p.Kind)
	if err != nil {
		return nil, "", err
	}
	form := url.Values{}
	form.Set("client_id", p.Config.ClientID)
	if p.Config.ClientSecret != "" {
		form.Set("client_secret", p.Config.ClientSecret)
	}
	form.Set("device_code", deviceCode)
	form.Set("grant_type", grantTypeFor(p.Kind))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var payload struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", fmt.Errorf("decode token response: %w (body: %q)", err, string(body))
	}
	if payload.Error != "" {
		return nil, payload.Error, nil
	}
	if payload.AccessToken == "" {
		return nil, "", fmt.Errorf("token response missing access_token (status %d)", resp.StatusCode)
	}
	tok := &oauth2.Token{
		AccessToken:  payload.AccessToken,
		TokenType:    payload.TokenType,
		RefreshToken: payload.RefreshToken,
	}
	if payload.ExpiresIn > 0 {
		tok.Expiry = s.now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	if payload.Scope != "" {
		tok = tok.WithExtra(map[string]any{"scope": payload.Scope})
	}
	return tok, "", nil
}

func deviceCodeEndpoint(kind auth.OAuthProviderKind) (string, error) {
	switch kind {
	case auth.OAuthGoogle:
		return auth.GoogleDeviceCodeURL, nil
	case auth.OAuthMicrosoft:
		return auth.MicrosoftDeviceCodeURL, nil
	}
	return "", fmt.Errorf("devicecode: unknown provider %q", kind)
}

func tokenEndpoint(kind auth.OAuthProviderKind) (string, error) {
	switch kind {
	case auth.OAuthGoogle:
		return auth.GoogleDeviceTokenURL, nil
	case auth.OAuthMicrosoft:
		return auth.MicrosoftDeviceTokenURL, nil
	}
	return "", fmt.Errorf("devicecode: unknown provider %q", kind)
}

// grantTypeFor returns the device-flow grant_type each provider expects.
// Google sticks with the original draft URN; Microsoft uses the
// IANA-registered URN.
func grantTypeFor(kind auth.OAuthProviderKind) string {
	switch kind {
	case auth.OAuthGoogle:
		return "http://oauth.net/grant_type/device/1.0"
	default:
		return "urn:ietf:params:oauth:grant-type:device_code"
	}
}

func flowKey(id string) string {
	return keyFlowPrefix + id
}

func userKey(userID int64, provider auth.OAuthProviderKind) string {
	return keyUserPrefix + strconv.FormatInt(userID, 10) + ":" + string(provider)
}

func newFlowID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
