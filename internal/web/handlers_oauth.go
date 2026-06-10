package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/security/audit"
)

// oauthStateCookieName carries the short-lived (10 minute) state token that
// pins a /callback request to the matching /start request. Stops CSRF +
// replay across the OAuth dance.
const oauthStateCookieName = "nightms_oauth_state"

// OAuthProviders bundles the configured providers handed to the web layer
// from main. Either may be nil if its env vars aren't set; the linking UI
// hides the corresponding button when so.
type OAuthProviders struct {
	Google    *auth.OAuthProvider
	Microsoft *auth.OAuthProvider
}

// providerByKey resolves the URL param ("google" / "microsoft") to the
// configured provider, or nil if unconfigured/unknown.
func (h *handlers) providerByKey(key string) *auth.OAuthProvider {
	switch strings.ToLower(key) {
	case "google":
		return h.oauth.Google
	case "microsoft", "ms":
		return h.oauth.Microsoft
	}
	return nil
}

// oauthStart handles /auth/{provider}/start. OAuth is link-only now — an
// active session is required; anonymous visitors are sent to /login first.
// Generates a state token, sets the short-lived state cookie, and redirects
// to the IdP.
func (h *handlers) oauthStart(w http.ResponseWriter, r *http.Request) {
	provider := h.providerByKey(chi.URLParam(r, "provider"))
	if provider == nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	if identityFrom(r) == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	stateRaw := make([]byte, 32)
	if _, err := rand.Read(stateRaw); err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(stateRaw)
	stateBlob := oauthState{
		State:    state,
		Provider: string(provider.Kind),
	}
	body, _ := json.Marshal(stateBlob)
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(body),
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(10 * time.Minute),
	})
	http.Redirect(w, r, provider.AuthCodeURL(state), http.StatusSeeOther)
}

type oauthState struct {
	State    string `json:"state"`
	Provider string `json:"provider"`
}

// oauthCallback handles /auth/{provider}/callback. Validates the state
// cookie, exchanges the code, fetches userinfo, then attaches the credential
// to the active session. Three resolved outcomes:
//   - existing credential owned by THIS user → re-auth path, token row
//     gets updated (handled in Phase 2 — token persistence).
//   - existing credential owned by ANOTHER user → "linked elsewhere" error.
//   - no existing credential → fresh link to this user.
func (h *handlers) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider := h.providerByKey(chi.URLParam(r, "provider"))
	if provider == nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	active := identityFrom(r)
	if active == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie(oauthStateCookieName)
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	defer h.clearCookie(w, oauthStateCookieName)
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		http.Error(w, "bad state cookie", http.StatusBadRequest)
		return
	}
	var stored oauthState
	if err := json.Unmarshal(raw, &stored); err != nil {
		http.Error(w, "bad state payload", http.StatusBadRequest)
		return
	}
	if stored.State != r.URL.Query().Get("state") {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	if stored.Provider != string(provider.Kind) {
		http.Error(w, "state provider mismatch", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, user, err := provider.Exchange(ctx, code)
	if err != nil {
		h.deps.Logger.Warn("oauth: exchange failed", "provider", provider.Kind, "err", err)
		http.Error(w, "oauth exchange failed", http.StatusBadGateway)
		return
	}

	lookup, existing, err := auth.ResolveExistingLink(ctx, h.deps.Queries, active.UserID, provider.Kind, user.Subject)
	if err != nil {
		h.deps.Logger.Error("oauth: cred lookup", "err", err)
		http.Error(w, "credential lookup failed", http.StatusInternalServerError)
		return
	}

	switch lookup {
	case auth.LinkLookupSameUser:
		// Re-auth: upsert the token row. Token columns NULL when the
		// sealer isn't configured — surface a clearer error than a SQL
		// failure would give.
		if h.deps.OAuthSealer == nil {
			h.flash(w, "error", "Token encryption isn't configured — set NIGHTMS_OAUTH_TOKEN_SECRET or restart.")
			http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
			return
		}
		if err := upsertOAuthTokenRow(ctx, h.deps.Queries, h.deps.OAuthSealer, existing.ID, tok); err != nil {
			h.deps.Logger.Error("oauth: upsert token", "err", err)
			h.flash(w, "error", "Failed to update tokens: "+err.Error())
			http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
			return
		}
		_ = h.deps.Queries.TouchCredentialLastUsed(ctx, gen.TouchCredentialLastUsedParams{
			ID:         existing.ID,
			LastUsedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		h.emitOAuthAudit(ctx, active.UserID, r, audit.OAuthLinked{
			Provider:        string(provider.Kind),
			ProviderSubject: user.Subject,
			Method:          "browser",
		})
		h.flash(w, "info", fmt.Sprintf("%s account re-authorized.", provider.Kind))
		http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
		return
	case auth.LinkLookupOtherUser:
		h.flash(w, "error", fmt.Sprintf("That %s account is linked to a different handle.", provider.Kind))
		http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
		return
	}

	// Fresh link: insert credential + insert token in one transaction so
	// a partial failure leaves no orphaned credential row.
	if h.deps.OAuthSealer == nil {
		h.flash(w, "error", "Token encryption isn't configured — set NIGHTMS_OAUTH_TOKEN_SECRET or restart.")
		http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
		return
	}
	if err := h.linkOAuthCredential(ctx, active.UserID, provider.Kind, user, tok); err != nil {
		h.deps.Logger.Error("oauth: link insert", "err", err)
		h.flash(w, "error", "Failed to link account: "+err.Error())
		http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
		return
	}
	h.emitOAuthAudit(ctx, active.UserID, r, audit.OAuthLinked{
		Provider:        string(provider.Kind),
		ProviderSubject: user.Subject,
		Method:          "browser",
	})

	// Linking a new credential is an auth-change — revoke other sessions
	// and re-mint this one so the user stays authenticated on the page
	// they just used.
	if err := h.sessions.ClearAllForUser(ctx, active.UserID); err != nil {
		h.deps.Logger.Warn("oauth link: clear other sessions", "user_id", active.UserID, "err", err)
	}
	if _, err := h.sessions.Set(ctx, r, w, active.UserID); err != nil {
		h.deps.Logger.Error("oauth link: mint new session", "user_id", active.UserID, "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.flash(w, "info", fmt.Sprintf("%s account linked.", provider.Kind))
	http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
}

// linkOAuthCredential inserts the credential + oauth_tokens rows in one
// transaction so neither lands without the other.
func (h *handlers) linkOAuthCredential(ctx context.Context, userID int64, kind auth.OAuthProviderKind, user auth.OAuthUser, tok *oauth2.Token) error {
	metadata, _ := json.Marshal(map[string]any{
		"email": user.Email,
		"name":  user.Name,
	})
	label := "linked from /profile"
	tx, err := h.deps.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := h.deps.Queries.WithTx(tx)
	cred, err := q.InsertOAuthCredential(ctx, gen.InsertOAuthCredentialParams{
		UserID:    userID,
		Provider:  string(kind),
		Subject:   user.Subject,
		Metadata:  metadata,
		Label:     &label,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return err
	}
	if err := insertOAuthTokenRow(ctx, q, h.deps.OAuthSealer, cred.ID, tok); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// insertOAuthTokenRow is the initial-link token write. Used by
// linkOAuthCredential inside its transaction.
func insertOAuthTokenRow(ctx context.Context, q *gen.Queries, sealer interface {
	Seal([]byte) []byte
}, credID int64, tok *oauth2.Token) error {
	sealedAccess := sealer.Seal([]byte(tok.AccessToken))
	var sealedRefresh []byte
	if tok.RefreshToken != "" {
		sealedRefresh = sealer.Seal([]byte(tok.RefreshToken))
	}
	exp := tok.Expiry
	now := time.Now().UTC()
	if exp.IsZero() {
		exp = now.Add(time.Hour)
	}
	tokenType := tok.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	var scopes []string
	if raw, ok := tok.Extra("scope").(string); ok && raw != "" {
		scopes = strings.Fields(raw)
	}
	return q.InsertOAuthToken(ctx, gen.InsertOAuthTokenParams{
		CredentialID:          credID,
		EncryptedAccessToken:  sealedAccess,
		EncryptedRefreshToken: sealedRefresh,
		AccessExpiresAt:       pgtype.Timestamptz{Time: exp, Valid: true},
		Scopes:                scopes,
		TokenType:             tokenType,
		CreatedAt:             pgtype.Timestamptz{Time: now, Valid: true},
	})
}

// upsertOAuthTokenRow replaces the token row for an existing credential
// (re-auth path).
func upsertOAuthTokenRow(ctx context.Context, q *gen.Queries, sealer interface {
	Seal([]byte) []byte
}, credID int64, tok *oauth2.Token) error {
	sealedAccess := sealer.Seal([]byte(tok.AccessToken))
	var sealedRefresh []byte
	if tok.RefreshToken != "" {
		sealedRefresh = sealer.Seal([]byte(tok.RefreshToken))
	}
	exp := tok.Expiry
	now := time.Now().UTC()
	if exp.IsZero() {
		exp = now.Add(time.Hour)
	}
	tokenType := tok.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	var scopes []string
	if raw, ok := tok.Extra("scope").(string); ok && raw != "" {
		scopes = strings.Fields(raw)
	}
	return q.UpsertOAuthToken(ctx, gen.UpsertOAuthTokenParams{
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

// emitOAuthAudit best-effort records an OAuth-related audit event with the
// caller's Handle + IP filled in from the request.
func (h *handlers) emitOAuthAudit(ctx context.Context, userID int64, r *http.Request, ev audit.Event) {
	if h.deps.Audit == nil {
		return
	}
	// We need the handle for the event; fetch it once. Best-effort — a
	// missed audit event is preferable to failing the link.
	user, err := h.deps.Queries.GetUserByID(ctx, userID)
	ip := remoteAddrIP(r)
	switch e := ev.(type) {
	case audit.OAuthLinked:
		if err == nil {
			e.Handle = user.Handle
		}
		e.IP = ip
		h.deps.Audit.Record(ctx, e)
	case audit.OAuthUnlinked:
		if err == nil {
			e.Handle = user.Handle
		}
		e.IP = ip
		h.deps.Audit.Record(ctx, e)
	default:
		h.deps.Audit.Record(ctx, ev)
	}
}

// remoteAddrIP extracts a presentation IP from the request. r.RemoteAddr is
// "ip:port"; we want just the ip. Falls back to the raw string if SplitHostPort
// fails (unlikely in normal traffic).
func remoteAddrIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// clearCookie wipes the named cookie. Used for the short-lived OAuth state
// cookie after the callback runs (success or failure).
func (h *handlers) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// flash writes a one-shot status into a short-lived cookie that the next
// page render picks up and clears. Used for "Linked!" / "Failed" feedback
// after the OAuth callback redirects back into /profile/connections.
func (h *handlers) flash(w http.ResponseWriter, kind, text string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "nightms_flash",
		Value:    base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + text)),
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(60 * time.Second),
	})
}

// readFlash consumes the one-shot flash cookie (if any) and returns the
// kind+text pair for templates to render.
func (h *handlers) readFlash(w http.ResponseWriter, r *http.Request) (kind, text string) {
	c, err := r.Cookie("nightms_flash")
	if err != nil {
		return "", ""
	}
	h.clearCookie(w, "nightms_flash")
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
