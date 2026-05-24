package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// renameData is the /profile/rename view-model.
type renameData struct {
	pageData
	Current string
	Notice  string
	Kind    string
}

// renameGet renders the form, prefilled with the current handle.
func (h *handlers) renameGet(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	page := h.basePage(r, "rename handle")
	page.Rename = &renameData{Current: id.Handle}
	h.render(w, "rename", page)
}

// renamePost updates the handle inside a transaction so the audit row
// rides along. Friendly errors for the common collision + invalid-format
// cases; the unique index is the catch-all backstop.
func (h *handlers) renamePost(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	next := strings.TrimSpace(r.PostFormValue("handle"))
	if !isValidHandle(next) {
		h.renderRenameError(w, r, id.Handle, "Handle must be 3-32 chars: letters, digits, underscore, dash.")
		return
	}
	if strings.EqualFold(next, id.Handle) {
		// No-op rename — treat as success so the UX is gentle.
		http.Redirect(w, r, "/profile?ok=renamed", http.StatusSeeOther)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// Pre-check for a friendly message; the unique index still backstops
	// races between two simultaneous renames.
	_, err := h.deps.Queries.GetUserByHandle(ctx, next)
	if err == nil {
		h.renderRenameError(w, r, id.Handle, "That handle is already taken.")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		h.deps.Logger.Error("rename: lookup", "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}

	tx, err := h.deps.Pool.Begin(ctx)
	if err != nil {
		http.Error(w, "begin tx", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	q := h.deps.Queries.WithTx(tx)

	if err := q.RenameUserHandle(ctx, gen.RenameUserHandleParams{ID: id.UserID, Handle: next}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			h.renderRenameError(w, r, id.Handle, "That handle is already taken.")
			return
		}
		h.deps.Logger.Error("rename: update", "err", err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}

	details, _ := json.Marshal(map[string]any{
		"from": id.Handle,
		"to":   next,
	})
	uid := id.UserID
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (actor_id, action, target_type, target_id, details, created_at)
		VALUES ($1, 'user.rename', 'user', $1, $2, $3)`,
		uid, details, pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true})
	if err != nil {
		h.deps.Logger.Warn("rename: audit", "err", err)
	}
	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "commit", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/profile?ok=renamed", http.StatusSeeOther)
}

func (h *handlers) renderRenameError(w http.ResponseWriter, r *http.Request, current, msg string) {
	page := h.basePage(r, "rename handle")
	page.Rename = &renameData{Current: current, Notice: msg, Kind: "err"}
	w.WriteHeader(http.StatusBadRequest)
	h.render(w, "rename", page)
}
