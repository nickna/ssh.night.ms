package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// connectionsRow is the per-row template binding for /profile/connections.
// Pulled from identity_credentials and bucketed by provider so the page
// can show the right "link Google / Microsoft" CTAs alongside what's
// already attached.
type connectionsRow struct {
	ID          int64
	Provider    string
	Subject     string
	Label       string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	IsSSH       bool
}

// connectionsData is the page view-model.
type connectionsData struct {
	pageData
	Rows                []connectionsRow
	GoogleLinked        bool
	MicrosoftLinked     bool
	GoogleAvailable     bool
	MicrosoftAvailable  bool
	FlashKind, FlashMsg string
}

// connectionsView renders the page. Reads identity_credentials for the
// active user, partitions by provider for the link buttons.
func (h *handlers) connectionsView(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	creds, err := h.deps.Queries.ListCredentialsForUser(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("connections: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	page := connectionsData{
		pageData:           h.basePage(r, "connected accounts"),
		GoogleAvailable:    h.oauth.Google != nil,
		MicrosoftAvailable: h.oauth.Microsoft != nil,
	}
	for _, c := range creds {
		row := connectionsRow{
			ID:        c.ID,
			Provider:  c.Provider,
			Subject:   c.Subject,
			CreatedAt: c.CreatedAt.Time,
			IsSSH:     c.Provider == "Ssh",
		}
		if c.Label != nil {
			row.Label = *c.Label
		}
		if c.LastUsedAt.Valid {
			t := c.LastUsedAt.Time
			row.LastUsedAt = &t
		}
		switch c.Provider {
		case "Google":
			page.GoogleLinked = true
		case "Microsoft":
			page.MicrosoftLinked = true
		}
		page.Rows = append(page.Rows, row)
	}
	kind, msg := h.readFlash(w, r)
	page.FlashKind = kind
	page.FlashMsg = msg
	h.render(w, "connections", page.pageData.withConnections(page))
}

// withConnections stashes the connections sub-view-model in pageData so the
// template can reach it via $.Connections. Keeps the basePage shape the
// other pages already render against.
func (p pageData) withConnections(c connectionsData) pageData {
	p.Connections = &c
	return p
}

// connectionsUnlink handles DELETE-style removal of one credential by id.
// Refuses to unlink the user's last credential when no password is set,
// since that would lock them out.
func (h *handlers) connectionsUnlink(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	idParam := strings.TrimSpace(chi.URLParam(r, "id"))
	credID, err := strconv.ParseInt(idParam, 10, 64)
	if err != nil || credID <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	user, err := h.deps.Queries.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		http.Error(w, "lookup", http.StatusInternalServerError)
		return
	}
	creds, _ := h.deps.Queries.ListCredentialsForUser(r.Context(), id.UserID)
	if len(user.PasswordHash) == 0 && len(creds) <= 1 {
		h.flash(w, "error", "Can't unlink your last credential — set a password first.")
		http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
		return
	}
	rows, err := h.deps.Queries.DeleteCredentialByID(r.Context(), gen.DeleteCredentialByIDParams{
		ID:     credID,
		UserID: id.UserID,
	})
	if err != nil || rows == 0 {
		h.flash(w, "error", "Credential not found.")
		http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
		return
	}
	// Unlinking is an auth-change. Treat it like a password change:
	// revoke every other session, then mint a fresh one for this
	// browser so the user isn't booted out of the page they just used.
	if err := h.sessions.ClearAllForUser(r.Context(), id.UserID); err != nil {
		h.deps.Logger.Warn("unlink: clear other sessions", "user_id", id.UserID, "err", err)
	}
	if _, err := h.sessions.Set(r.Context(), r, w, id.UserID); err != nil {
		h.deps.Logger.Error("unlink: mint new session", "user_id", id.UserID, "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.flash(w, "info", "Credential unlinked.")
	http.Redirect(w, r, "/profile/connections", http.StatusSeeOther)
}
