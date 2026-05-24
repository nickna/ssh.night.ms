package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// oauthStateCookieName carries the short-lived (10 minute) state token that
// pins a /callback request to the matching /start request. Stops CSRF +
// replay across the OAuth dance.
const oauthStateCookieName = "nightms_oauth_state"

// oauthFinishCookieName carries the OAuthUser payload that survived a
// callback when the subject was unknown. The user lands on /auth/finish to
// pick a handle, and the cookie supplies the provider/subject/email so we
// don't have to round-trip back to the IdP.
const oauthFinishCookieName = "nightms_oauth_finish"

// OAuthProviders bundles the configured providers handed to the web layer
// from main. Either may be nil if its env vars aren't set; the login page
// hides the corresponding button when so.
type OAuthProviders struct {
	Google    *auth.OAuthProvider
	Microsoft *auth.OAuthProvider
}

// Has returns true if any provider is configured — the login template uses
// this to decide whether to render the "or sign in with" divider.
func (p OAuthProviders) Has() bool { return p.Google != nil || p.Microsoft != nil }

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

// oauthStart handles /auth/{provider}/start. Generates a state token,
// sets the corresponding short-lived cookie, and redirects to the IdP.
// `link=1` query stashes the linkage intent in the state cookie so the
// callback knows to attach to the current session instead of logging in.
func (h *handlers) oauthStart(w http.ResponseWriter, r *http.Request) {
	provider := h.providerByKey(chi.URLParam(r, "provider"))
	if provider == nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	link := r.URL.Query().Get("link") == "1"
	if link && identityFrom(r) == nil {
		// Linking requires an active session — otherwise this would attach
		// the OAuth to an empty principal.
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
		Link:     link,
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
	Link     bool   `json:"link"`
}

// oauthCallback handles /auth/{provider}/callback. Validates the state
// cookie, exchanges the code, fetches userinfo, then either:
//   - logs the user in if the (provider, subject) is already linked,
//   - attaches the credential to the active session if Link==true,
//   - stashes the userinfo in oauthFinishCookieName and redirects to
//     /auth/finish where the user picks a handle to create an account.
func (h *handlers) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider := h.providerByKey(chi.URLParam(r, "provider"))
	if provider == nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
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
	_, user, err := provider.Exchange(ctx, code)
	if err != nil {
		h.deps.Logger.Warn("oauth: exchange failed", "provider", provider.Kind, "err", err)
		http.Error(w, "oauth exchange failed", http.StatusBadGateway)
		return
	}

	// Already linked? Log in (or no-op if this is a link request).
	cred, lookupErr := h.deps.Queries.GetCredentialByProviderSubject(ctx, gen.GetCredentialByProviderSubjectParams{
		Provider: string(provider.Kind),
		Subject:  user.Subject,
	})
	if lookupErr == nil {
		if stored.Link {
			// Already attached — to whom?
			active := identityFrom(r)
			if active != nil && cred.UserID == active.UserID {
				h.flash(w, "info", "Already linked.")
				http.Redirect(w, r, "/profile", http.StatusSeeOther)
				return
			}
			h.flash(w, "error", fmt.Sprintf("That %s account is linked to a different handle.", provider.Kind))
			http.Redirect(w, r, "/profile", http.StatusSeeOther)
			return
		}
		// Touch last_used + sign in.
		_ = h.deps.Queries.TouchCredentialLastUsed(ctx, gen.TouchCredentialLastUsedParams{
			ID:         cred.ID,
			LastUsedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if _, err := h.sessions.Set(ctx, r, w, cred.UserID); err != nil {
			h.deps.Logger.Error("oauth: create session", "user_id", cred.UserID, "err", err)
			http.Error(w, "could not create session", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		h.deps.Logger.Error("oauth: cred lookup", "err", lookupErr)
		http.Error(w, "credential lookup failed", http.StatusInternalServerError)
		return
	}

	// Unknown subject. Two cases:
	if stored.Link {
		// Linking flow: attach the new credential to the current session.
		active := identityFrom(r)
		if active == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if err := h.insertOAuthCredential(ctx, active.UserID, provider.Kind, user); err != nil {
			h.deps.Logger.Error("oauth: link insert", "err", err)
			h.flash(w, "error", "Failed to link account: "+err.Error())
			http.Redirect(w, r, "/profile", http.StatusSeeOther)
			return
		}
		// Linking a new credential is an auth-change. Match the
		// password-change + unlink behavior: revoke every other session,
		// re-mint this one so the user stays on /profile already
		// authenticated.
		if err := h.sessions.ClearAllForUser(ctx, active.UserID); err != nil {
			h.deps.Logger.Warn("oauth link: clear other sessions", "user_id", active.UserID, "err", err)
		}
		if _, err := h.sessions.Set(ctx, r, w, active.UserID); err != nil {
			h.deps.Logger.Error("oauth link: mint new session", "user_id", active.UserID, "err", err)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h.flash(w, "info", fmt.Sprintf("%s account linked.", provider.Kind))
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}

	// Signup flow: park the userinfo in a finish cookie + redirect to the
	// "pick a handle" page.
	finish := oauthFinishPayload{
		Provider: string(provider.Kind),
		Subject:  user.Subject,
		Email:    user.Email,
		Name:     user.Name,
	}
	body, _ := json.Marshal(finish)
	http.SetCookie(w, &http.Cookie{
		Name:     oauthFinishCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(body),
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(10 * time.Minute),
	})
	http.Redirect(w, r, "/auth/finish", http.StatusSeeOther)
}

type oauthFinishPayload struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// oauthFinishGet renders the "pick a handle" form for first-time OAuth
// signups. Pre-populates the handle field from the email user-part if
// present.
func (h *handlers) oauthFinishGet(w http.ResponseWriter, r *http.Request) {
	finish, ok := h.readFinishCookie(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	page := h.basePage(r, "finish signup")
	page.OAuthFinish = &oauthFinishView{
		Provider:        finish.Provider,
		Email:           finish.Email,
		Name:            finish.Name,
		SuggestedHandle: suggestedHandle(finish.Email, finish.Name, finish.Subject),
	}
	h.render(w, "oauth_finish", page)
}

// oauthFinishPost validates the chosen handle, creates the user + the OAuth
// credential in one transaction (via auth.CreateAccount with no password +
// the offered identity_credential), and signs the user in.
func (h *handlers) oauthFinishPost(w http.ResponseWriter, r *http.Request) {
	finish, ok := h.readFinishCookie(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Signups kill switch — matches the SSH-side gate in auth.CreateAccount.
	// Settings.Cache is shared between transport + web so toggling
	// signups_enabled from the sysop console closes both surfaces in lockstep.
	if sc := h.deps.Session.Security.Settings; sc != nil && !sc.Get().SignupsEnabled {
		h.renderFinishError(w, r, finish, sc.Get().SignupsDisabledMessage)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	handle := strings.TrimSpace(r.PostFormValue("handle"))

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Reject taken handles before touching anything. Race with concurrent
	// signups is still caught by the unique index — we just give a friendlier
	// error here on the common path.
	_, lookupErr := h.deps.Queries.GetUserByHandle(ctx, handle)
	if lookupErr == nil {
		h.renderFinishError(w, r, finish, "handle is already taken")
		return
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		h.deps.Logger.Error("oauth finish: lookup", "err", lookupErr)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if !isValidHandle(handle) {
		h.renderFinishError(w, r, finish, "handle must be 3-32 chars: letters, digits, underscore, dash")
		return
	}

	// Create the user with no password (RequireSshKey=false; they can add
	// one later from /profile). Single transaction: users insert + credential
	// insert + audit row.
	tx, err := h.deps.Pool.Begin(ctx)
	if err != nil {
		http.Error(w, "begin tx failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	q := h.deps.Queries.WithTx(tx)

	now := time.Now().UTC()
	var userID int64
	err = tx.QueryRow(ctx, `INSERT INTO users (
		handle, created_at, last_seen_at, is_sysop, is_banned,
		clock_format, date_format, temperature_unit, time_zone_id, location_source,
		password_hash, password_algo, password_updated_at,
		suppress_key_adoption_prompts, require_ssh_key
	) VALUES (
		$1, $2, $2, FALSE, FALSE,
		0, 0, 0, 'UTC', 0,
		NULL, NULL, NULL,
		FALSE, FALSE
	) RETURNING id`, handle, now).Scan(&userID)
	if err != nil {
		h.deps.Logger.Warn("oauth finish: user insert", "err", err)
		h.renderFinishError(w, r, finish, "couldn't create account: "+err.Error())
		return
	}

	metadata, _ := json.Marshal(map[string]any{
		"email": finish.Email,
		"name":  finish.Name,
	})
	if _, err := q.InsertOAuthCredential(ctx, gen.InsertOAuthCredentialParams{
		UserID:    userID,
		Provider:  finish.Provider,
		Subject:   finish.Subject,
		Metadata:  metadata,
		Label:     ptrStr("linked at signup"),
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		h.deps.Logger.Warn("oauth finish: cred insert", "err", err)
		h.renderFinishError(w, r, finish, "couldn't link OAuth credential")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "commit failed", http.StatusInternalServerError)
		return
	}
	h.clearCookie(w, oauthFinishCookieName)
	if _, err := h.sessions.Set(ctx, r, w, userID); err != nil {
		h.deps.Logger.Error("oauth: create session", "user_id", userID, "err", err)
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderFinishError re-renders the form with the chosen handle + an error,
// so the user can fix and retry without re-running the OAuth dance.
func (h *handlers) renderFinishError(w http.ResponseWriter, r *http.Request, finish oauthFinishPayload, msg string) {
	page := h.basePage(r, "finish signup")
	page.OAuthFinish = &oauthFinishView{
		Provider:        finish.Provider,
		Email:           finish.Email,
		Name:            finish.Name,
		SuggestedHandle: suggestedHandle(finish.Email, finish.Name, finish.Subject),
	}
	page.Error = msg
	w.WriteHeader(http.StatusBadRequest)
	h.render(w, "oauth_finish", page)
}

// readFinishCookie pulls the parked OAuth payload off the finish cookie.
// Returns (zero, false) on missing / malformed cookie.
func (h *handlers) readFinishCookie(r *http.Request) (oauthFinishPayload, bool) {
	c, err := r.Cookie(oauthFinishCookieName)
	if err != nil {
		return oauthFinishPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return oauthFinishPayload{}, false
	}
	var p oauthFinishPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return oauthFinishPayload{}, false
	}
	if p.Provider == "" || p.Subject == "" {
		return oauthFinishPayload{}, false
	}
	return p, true
}

// insertOAuthCredential writes one row through the generic insert query.
// Used by the link-mid-session path; the signup path uses its own tx so it
// can roll back user + credential together.
func (h *handlers) insertOAuthCredential(ctx context.Context, userID int64, kind auth.OAuthProviderKind, user auth.OAuthUser) error {
	metadata, _ := json.Marshal(map[string]any{
		"email": user.Email,
		"name":  user.Name,
	})
	_, err := h.deps.Queries.InsertOAuthCredential(ctx, gen.InsertOAuthCredentialParams{
		UserID:    userID,
		Provider:  string(kind),
		Subject:   user.Subject,
		Metadata:  metadata,
		Label:     ptrStr("linked from /profile"),
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	return err
}

// ptrStr is a tiny helper for sqlc-nullable string fields.
func ptrStr(s string) *string { return &s }

// clearCookie wipes the named cookie. Used for the short-lived OAuth state
// + finish cookies — both should die after their corresponding handler
// runs regardless of outcome.
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
// after the OAuth callback redirects back into /profile.
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

// suggestedHandle picks a sane default for the "finish signup" form. Tries
// the email user-part first (cleaned of invalid chars), falls back to the
// display name, and finally to a truncated subject hash. The user can edit
// before submitting.
func suggestedHandle(email, name, subject string) string {
	for _, candidate := range []string{
		emailUserPart(email),
		cleanHandle(name),
		"user" + cleanHandle(subject)[:min(6, len(cleanHandle(subject)))],
	} {
		if isValidHandle(candidate) {
			return candidate
		}
	}
	return ""
}

func emailUserPart(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return cleanHandle(email[:i])
	}
	return cleanHandle(email)
}

// cleanHandle keeps only the chars isValidHandle allows. Truncates to 32.
func cleanHandle(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}

// isValidHandle mirrors auth.isValidHandle (which is unexported); keeping a
// local copy so handlers don't depend on auth internals.
func isValidHandle(handle string) bool {
	if len(handle) < 3 || len(handle) > 32 {
		return false
	}
	for _, r := range handle {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}
