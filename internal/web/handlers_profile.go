package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	gossh "golang.org/x/crypto/ssh"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// profileData is the view-model for /profile. We collapse a few sqlc shapes
// into plain Go types so the template stays simple.
type profileData struct {
	pageData
	HandleCreated time.Time
	LastSeen      *time.Time
	SSHKeyCount   int
	Notice        string
	NoticeKind    string // "" / "ok" / "err"
}

func (h *handlers) profileView(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	user, err := h.deps.Queries.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("profile: load user", "user_id", id.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	keys, _ := h.deps.Queries.ListSshCredentialsForUser(r.Context(), id.UserID)

	data := profileData{
		pageData:      h.basePage(r, "profile"),
		HandleCreated: user.CreatedAt.Time,
		SSHKeyCount:   len(keys),
	}
	if user.LastSeenAt.Valid {
		t := user.LastSeenAt.Time
		data.LastSeen = &t
	}
	// One-shot notice handed off across redirects via a flash query param.
	// Simpler than a stash cookie; the param is meaningless to forge since
	// it doesn't unlock any privilege.
	switch r.URL.Query().Get("ok") {
	case "password":
		data.Notice = "password updated"
		data.NoticeKind = "ok"
	case "key_added":
		data.Notice = "ssh key added"
		data.NoticeKind = "ok"
	case "key_removed":
		data.Notice = "ssh key removed"
		data.NoticeKind = "ok"
	case "picture":
		data.Notice = "profile picture updated"
		data.NoticeKind = "ok"
	case "picture_cleared":
		data.Notice = "profile picture cleared — back to identicon"
		data.NoticeKind = "ok"
	}
	h.renderProfile(w, "profile", data)
}

type passwordFormData struct {
	pageData
	Error string
}

func (h *handlers) passwordGet(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r) == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.renderProfile(w, "profile_password", passwordFormData{pageData: h.basePage(r, "change password")})
}

func (h *handlers) passwordPost(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	current := r.PostFormValue("current")
	next := r.PostFormValue("new")
	confirm := r.PostFormValue("confirm")

	render := func(msg string) {
		w.WriteHeader(http.StatusUnauthorized)
		h.renderProfile(w, "profile_password", passwordFormData{
			pageData: h.basePage(r, "change password"),
			Error:    msg,
		})
	}

	if next == "" || next != confirm {
		render("new passwords don't match")
		return
	}
	if len(next) < 10 {
		render("new password must be at least 10 characters")
		return
	}

	// Verify current via the same hasher path SSH/web logins use. Loading
	// the user row gives us PasswordHash / PasswordAlgo even though we
	// already have an identity in context.
	user, err := h.deps.Queries.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("password change: load user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(user.PasswordHash) == 0 {
		render("account has no password set — contact a sysop")
		return
	}
	algo := ""
	if user.PasswordAlgo != nil {
		algo = *user.PasswordAlgo
	}
	res := h.deps.Hasher.Verify(current, user.PasswordHash, algo)
	if !res.OK {
		render("current password is wrong")
		return
	}

	// Hash + write the new password. The PHC format embeds the algorithm,
	// so the legacy algo column receives NULL via *string nil pointer.
	fresh, freshAlgo, err := h.deps.Hasher.Hash(next)
	if err != nil {
		h.deps.Logger.Error("password change: hash", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var algoPtr *string
	if freshAlgo != "" {
		algoPtr = &freshAlgo
	}
	if err := h.deps.Queries.UpdateUserPassword(r.Context(), gen.UpdateUserPasswordParams{
		ID:                user.ID,
		PasswordHash:      fresh,
		PasswordAlgo:      algoPtr,
		PasswordUpdatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}); err != nil {
		h.deps.Logger.Error("password change: update", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Also clear any lockout state so the user can immediately sign in
	// with the new password from another device.
	if h.deps.Lookup != nil && h.deps.Lookup.Limiter != nil {
		_ = h.deps.Lookup.Limiter.Clear(r.Context(), user.Handle)
	}
	// Revoke every other session for this user — a password change is the
	// canonical "I'm worried someone has my credentials" trigger and the
	// expected outcome is that every browser other than this one gets
	// kicked. Mint a fresh session for the current request afterward so
	// the user isn't booted out of the browser they just used to change
	// their password.
	if err := h.sessions.ClearAllForUser(r.Context(), user.ID); err != nil {
		h.deps.Logger.Warn("password change: clear other sessions", "user_id", user.ID, "err", err)
	}
	if _, err := h.sessions.Set(r.Context(), r, w, user.ID); err != nil {
		// Mint failed — best path is "send them to /login" rather than
		// leave them in a half-authed state.
		h.deps.Logger.Error("password change: mint new session", "user_id", user.ID, "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/profile?ok=password", http.StatusSeeOther)
}

//
// SSH key management
//

type sshKeyView struct {
	ID         int64
	Fingerprint string
	Algorithm  string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

type keysData struct {
	pageData
	Keys  []sshKeyView
	Error string
}

func (h *handlers) keysGet(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	rows, err := h.deps.Queries.ListSshCredentialsForUser(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("keys: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	view := make([]sshKeyView, 0, len(rows))
	for _, k := range rows {
		v := sshKeyView{
			ID:          k.ID,
			Fingerprint: k.Subject,
			CreatedAt:   k.CreatedAt.Time,
		}
		if k.Label != nil {
			v.Label = *k.Label
		}
		if k.LastUsedAt.Valid {
			t := k.LastUsedAt.Time
			v.LastUsedAt = &t
		}
		// Best-effort pull of algorithm from metadata so the user can tell
		// ed25519 from rsa at a glance. Failures are silently ignored.
		v.Algorithm = sshAlgorithmFromMetadata(k.Metadata)
		view = append(view, v)
	}
	h.renderProfile(w, "profile_keys", keysData{
		pageData: h.basePage(r, "ssh keys"),
		Keys:     view,
	})
}

func (h *handlers) keysAdd(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("public_key"))
	label := strings.TrimSpace(r.PostFormValue("label"))
	if label == "" {
		label = "untitled"
	}

	render := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		// Re-render keys page with error AND current list.
		rows, _ := h.deps.Queries.ListSshCredentialsForUser(r.Context(), id.UserID)
		view := make([]sshKeyView, 0, len(rows))
		for _, k := range rows {
			v := sshKeyView{ID: k.ID, Fingerprint: k.Subject, CreatedAt: k.CreatedAt.Time}
			if k.Label != nil {
				v.Label = *k.Label
			}
			if k.LastUsedAt.Valid {
				t := k.LastUsedAt.Time
				v.LastUsedAt = &t
			}
			v.Algorithm = sshAlgorithmFromMetadata(k.Metadata)
			view = append(view, v)
		}
		h.renderProfile(w, "profile_keys", keysData{
			pageData: h.basePage(r, "ssh keys"),
			Keys:     view,
			Error:    msg,
		})
	}

	pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(raw))
	if err != nil {
		render("not a recognizable OpenSSH public key")
		return
	}
	fingerprint := gossh.FingerprintSHA256(pubKey)
	algorithm := pubKey.Type()
	blob := pubKey.Marshal()
	metadata, err := json.Marshal(map[string]any{
		"algorithm": algorithm,
		"blob_b64":  base64.StdEncoding.EncodeToString(blob),
	})
	if err != nil {
		h.deps.Logger.Error("keys: marshal metadata", "err", err)
		render("internal error encoding key")
		return
	}

	labelPtr := &label
	_, err = h.deps.Queries.InsertSshCredential(r.Context(), gen.InsertSshCredentialParams{
		UserID:    id.UserID,
		Subject:   fingerprint,
		Metadata:  metadata,
		Label:     labelPtr,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		// Most likely path: unique violation on ix_identity_credentials_provider_subject
		// because the key is already on file (any user, since the index is global).
		if isUniqueViolation(err) {
			render("this public key is already registered")
			return
		}
		h.deps.Logger.Error("keys: insert", "err", err)
		render("internal error storing key")
		return
	}
	http.Redirect(w, r, "/profile?ok=key_added", http.StatusSeeOther)
}

//
// Profile picture upload
//

type pictureFormData struct {
	pageData
	HasUpload bool
	Error     string
}

func (h *handlers) pictureGet(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	user, err := h.deps.Queries.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("picture: load user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderProfile(w, "profile_picture", pictureFormData{
		pageData:  h.basePage(r, "profile picture"),
		HasUpload: user.ProfilePictureUpdatedAt.Valid,
	})
}

// pictureMaxBytes caps the upload at 5 MiB. Any larger and the
// browser-supplied image is almost certainly the wrong tool — the resizer
// just downscales, so we want users to use sensible source sizes.
const pictureMaxBytes = 5 << 20

func (h *handlers) picturePost(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	render := func(status int, msg string) {
		w.WriteHeader(status)
		user, _ := h.deps.Queries.GetUserByID(r.Context(), id.UserID)
		h.renderProfile(w, "profile_picture", pictureFormData{
			pageData:  h.basePage(r, "profile picture"),
			HasUpload: user.ProfilePictureUpdatedAt.Valid,
			Error:     msg,
		})
	}

	// Limit memory + the underlying body so a 5GB upload doesn't OOM us.
	r.Body = http.MaxBytesReader(w, r.Body, pictureMaxBytes+1024)
	if err := r.ParseMultipartForm(pictureMaxBytes); err != nil {
		render(http.StatusBadRequest, "upload too large or malformed (5MB max)")
		return
	}
	file, header, err := r.FormFile("picture")
	if err != nil {
		render(http.StatusBadRequest, "no file provided")
		return
	}
	defer file.Close()

	if header.Size > pictureMaxBytes {
		render(http.StatusBadRequest, "upload too large (5MB max)")
		return
	}

	if err := h.SaveProfilePicture(r.Context(), id.UserID, file); err != nil {
		h.deps.Logger.Error("picture: save", "user_id", id.UserID, "err", err)
		render(http.StatusBadRequest, "couldn't process that image — try PNG or JPEG")
		return
	}
	http.Redirect(w, r, "/profile?ok=picture", http.StatusSeeOther)
}

func (h *handlers) pictureClear(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := h.ClearProfilePicture(r.Context(), id.UserID); err != nil {
		h.deps.Logger.Error("picture: clear", "user_id", id.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/profile?ok=picture_cleared", http.StatusSeeOther)
}

func (h *handlers) keysDelete(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	idStr := chi.URLParam(r, "id")
	keyID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if _, err := h.deps.Queries.DeleteCredentialByID(r.Context(), gen.DeleteCredentialByIDParams{
		ID:     keyID,
		UserID: id.UserID,
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.deps.Logger.Error("keys: delete", "err", err)
		}
		// Either way, redirect — the owner guard means we never need to
		// distinguish "key didn't exist" from "key wasn't yours".
	}
	http.Redirect(w, r, "/profile?ok=key_removed", http.StatusSeeOther)
}

//
// helpers
//

// sessionsViewData is the view-model for /profile/sessions. Each row
// carries the SID (used to build the revoke form's action URL), a
// short browser/OS tag derived from the User-Agent, the raw UA for
// users who want the full string, and the captured IP. The Current
// flag marks the row authenticating this request itself so the UI can
// dim the revoke button.
type sessionsViewData struct {
	pageData
	Sessions []sessionsRow
}

type sessionsRow struct {
	SID       string
	UAShort   string
	UAFull    string
	IP        string
	CreatedAt time.Time
	LastSeen  time.Time
	Current   bool
}

func (h *handlers) sessionsView(w http.ResponseWriter, r *http.Request) {
	ident := identityFrom(r)
	if ident == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	rows, err := h.sessions.List(r.Context(), ident.UserID, h.sessions.CurrentSID(r))
	if err != nil {
		h.deps.Logger.Error("web: list sessions", "user_id", ident.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	view := sessionsViewData{pageData: h.basePage(r, "active sessions")}
	for _, row := range rows {
		view.Sessions = append(view.Sessions, sessionsRow{
			SID:       row.SID,
			UAShort:   shortUA(row.UA),
			UAFull:    row.UA,
			IP:        row.IP,
			CreatedAt: row.CreatedAt,
			LastSeen:  row.LastSeen,
			Current:   row.Current,
		})
	}
	h.renderProfile(w, "profile_sessions", view)
}

// sessionsRevoke kills one specific session by SID. Owner-guarded inside
// sessionStore.Revoke; a forged SID for someone else's session is a
// silent no-op. Revoking the current session lands you on the landing
// page already logged out; revoking another session just leaves you on
// the listing page.
func (h *handlers) sessionsRevoke(w http.ResponseWriter, r *http.Request) {
	ident := identityFrom(r)
	if ident == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	sid := chi.URLParam(r, "sid")
	if sid == "" {
		http.Redirect(w, r, "/profile/sessions", http.StatusSeeOther)
		return
	}
	if err := h.sessions.Revoke(r.Context(), ident.UserID, sid); err != nil {
		h.deps.Logger.Error("web: revoke session", "user_id", ident.UserID, "sid", sid, "err", err)
		http.Error(w, "could not revoke session", http.StatusInternalServerError)
		return
	}
	// Did the user revoke their own current session? If so, also clear
	// the cookie so the listing page doesn't redirect through a stale
	// auth-check on the way back to the landing page.
	if sid == h.sessions.CurrentSID(r) {
		h.sessions.Clear(r.Context(), r, w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/profile/sessions", http.StatusSeeOther)
}

// shortUA reduces a User-Agent string to a "Browser on OS" tag for
// display. Deliberately small — full UA strings are unreadable and the
// listing page already shows the raw UA on hover via the title attribute.
// Unmatched strings render as "Unknown browser."
func shortUA(ua string) string {
	if ua == "" {
		return "—"
	}
	browser := "Unknown browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		// Chrome detection comes after Edge because Edge's UA contains
		// both "Chrome/" and "Edg/".
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.Contains(ua, "curl/"):
		browser = "curl"
	case strings.Contains(ua, "wget"):
		browser = "wget"
	}
	os := ""
	// iOS + Android matches come BEFORE macOS / Linux because mobile UAs
	// embed those substrings ("like Mac OS X" on iOS, "Linux" on Android)
	// for backwards compat with old sniffers. Matching the device token
	// first avoids classifying every iPhone as "macOS."
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if os == "" {
		return browser
	}
	return browser + " on " + os
}

// renderProfile is a tiny convenience that mirrors the existing h.render but
// is typed loosely so the profile-specific data shapes above thread through.
// The shared layout template ultimately receives a value that embeds pageData,
// which is what the {{.Identity}} / {{.CSRFField}} accesses depend on.
func (h *handlers) renderProfile(w http.ResponseWriter, page string, data any) {
	tpl, ok := h.templates[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.deps.Logger.Error("web: render profile", "page", page, "err", err)
	}
}

// sshAlgorithmFromMetadata extracts the "algorithm" field if present. The
// jsonb column is a `[]byte` from sqlc; this is a best-effort decode.
func sshAlgorithmFromMetadata(meta []byte) string {
	if len(meta) == 0 {
		return ""
	}
	var m struct {
		Algorithm string `json:"algorithm"`
	}
	if json.Unmarshal(meta, &m) != nil {
		return ""
	}
	return m.Algorithm
}

// isUniqueViolation matches the pg SQLSTATE 23505 — used to distinguish
// "key already registered" from other DB errors when inserting credentials.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// pgx wraps the *pgconn.PgError in its returned errors; check by string
	// inspection rather than importing the type for a one-off compare.
	return strings.Contains(err.Error(), "SQLSTATE 23505") ||
		strings.Contains(err.Error(), "duplicate key value")
}

// Compile-time guard so the handlers package isn't trimmed by goimports
// when we add the field but don't reference it yet.
var _ = context.Background
var _ = fmt.Sprintf
