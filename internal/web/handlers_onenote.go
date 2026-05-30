package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nickna/ssh.night.ms/internal/auth/usertoken"
	"github.com/nickna/ssh.night.ms/internal/onenote"
)

// handlers_onenote.go is the /api/onenote JSON surface — the BBS's first REST
// API. Every handler resolves the in-process *onenote.Service (the same one
// the SSH TUI uses) and is cookie-authenticated via identityFrom. Mutating
// routes ride inside the existing CSRF group; the browser sends the gorilla/
// csrf token as the X-CSRF-Token header (see server.go).
//
// The typed errors from the OneNote/usertoken stack map to 409s with a
// machine-readable code so the client can prompt the user to link or
// re-authorize, never a bare 500.

const microsoftLinkURL = "/auth/microsoft/start"

// onenoteGuard resolves the authenticated user + the OneNote service, writing
// the appropriate JSON error and returning ok=false when either is missing.
func (h *handlers) onenoteGuard(w http.ResponseWriter, r *http.Request) (*onenote.Service, int64, bool) {
	id := identityFrom(r)
	if id == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
		return nil, 0, false
	}
	svc := h.deps.Session.Providers.OneNote
	if svc == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "OneNote integration is not configured")
		return nil, 0, false
	}
	return svc, id.UserID, true
}

// onenoteError maps an error from the service to a JSON response. Link/scope/
// reauth conditions become 409s carrying a CTA; Graph errors map by status;
// everything else is a logged 500.
func (h *handlers) onenoteError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, usertoken.ErrNoLink):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "not_linked", "message": "Link your Microsoft account to use OneNote.", "linkUrl": microsoftLinkURL})
	case errors.Is(err, usertoken.ErrMissingScope):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "reconsent_required", "message": "Re-authorize your Microsoft account to enable OneNote.", "linkUrl": microsoftLinkURL})
	case errors.Is(err, usertoken.ErrNeedsReauth):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "needs_reauth", "message": "Your Microsoft authorization expired. Re-authorize to continue.", "linkUrl": microsoftLinkURL})
	case errors.Is(err, onenote.ErrConfirmRequired):
		writeJSONError(w, http.StatusConflict, "confirm_required",
			"This page has images or tables a text rewrite would drop. Resend with confirmReplace to proceed.")
	default:
		var ge *onenote.GraphError
		if errors.As(err, &ge) {
			switch {
			case ge.StatusCode == http.StatusNotFound:
				writeJSONError(w, http.StatusNotFound, "not_found", "Not found.")
			case ge.StatusCode >= 500:
				writeJSONError(w, http.StatusBadGateway, "upstream_error", "OneNote service error.")
			default:
				writeJSONError(w, http.StatusBadRequest, "bad_request", ge.Message)
			}
			h.deps.Logger.Warn("onenote: graph error", "op", op, "status", ge.StatusCode, "code", ge.Code)
			return
		}
		h.deps.Logger.Error("onenote: "+op, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

// --- DTOs ----------------------------------------------------------------

type notebookDTO struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	IsDefault  bool      `json:"isDefault"`
	WebURL     string    `json:"webUrl,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type sectionDTO struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	NotebookID string    `json:"notebookId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type pageDTO struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	SectionID  string    `json:"sectionId,omitempty"`
	WebURL     string    `json:"webUrl,omitempty"`
	ClientURL  string    `json:"clientUrl,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type blockDTO struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

type elementDTO struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func toPageDTO(p onenote.Page) pageDTO {
	return pageDTO{
		ID: p.ID, Title: p.Title, SectionID: p.SectionID,
		WebURL: p.WebURL, ClientURL: p.ClientURL,
		CreatedAt: p.CreatedAt, ModifiedAt: p.ModifiedAt,
	}
}

// --- read handlers -------------------------------------------------------

func (h *handlers) onenoteNotebooks(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	nbs, err := svc.ListNotebooks(r.Context(), uid)
	if err != nil {
		h.onenoteError(w, "list notebooks", err)
		return
	}
	out := make([]notebookDTO, 0, len(nbs))
	for _, n := range nbs {
		out = append(out, notebookDTO{
			ID: n.ID, Name: n.Name, IsDefault: n.IsDefault, WebURL: n.WebURL,
			CreatedAt: n.CreatedAt, ModifiedAt: n.ModifiedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notebooks": out})
}

func (h *handlers) onenoteSections(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	secs, err := svc.ListSections(r.Context(), uid, r.URL.Query().Get("notebookId"))
	if err != nil {
		h.onenoteError(w, "list sections", err)
		return
	}
	out := make([]sectionDTO, 0, len(secs))
	for _, s := range secs {
		out = append(out, sectionDTO{
			ID: s.ID, Name: s.Name, NotebookID: s.NotebookID,
			CreatedAt: s.CreatedAt, ModifiedAt: s.ModifiedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sections": out})
}

func (h *handlers) onenotePages(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	pages, err := svc.ListPages(r.Context(), uid, r.URL.Query().Get("sectionId"))
	if err != nil {
		h.onenoteError(w, "list pages", err)
		return
	}
	out := make([]pageDTO, 0, len(pages))
	for _, p := range pages {
		out = append(out, toPageDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": out})
}

func (h *handlers) onenotePageGet(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	pageID := chi.URLParam(r, "id")
	pc, err := svc.GetPage(r.Context(), uid, pageID)
	if err != nil {
		h.onenoteError(w, "get page", err)
		return
	}
	blocks := make([]blockDTO, 0, len(pc.Blocks))
	for _, b := range pc.Blocks {
		blocks = append(blocks, blockDTO{Kind: b.Kind.String(), Text: b.Text, URL: b.URL})
	}
	elements := make([]elementDTO, 0, len(pc.Elements))
	for _, e := range pc.Elements {
		elements = append(elements, elementDTO{ID: e.ID, Kind: e.Kind.String(), Text: e.Text})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":       toPageDTO(pc.Page),
		"blocks":     blocks,
		"elements":   elements,
		"hasNonText": pc.HasNonText,
		"markdown":   pc.EditMarkdown(),
	})
}

func (h *handlers) onenoteRecent(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	recents, err := svc.ListRecentViewed(r.Context(), uid)
	if err != nil {
		h.onenoteError(w, "list recent", err)
		return
	}
	type recentDTO struct {
		PageID     string    `json:"pageId"`
		SectionID  string    `json:"sectionId,omitempty"`
		Title      string    `json:"title"`
		WebURL     string    `json:"webUrl,omitempty"`
		ModifiedAt time.Time `json:"modifiedAt"`
		ViewedAt   time.Time `json:"viewedAt"`
	}
	out := make([]recentDTO, 0, len(recents))
	for _, rp := range recents {
		out = append(out, recentDTO{
			PageID: rp.PageID, SectionID: rp.SectionID, Title: rp.Title,
			WebURL: rp.WebURL, ModifiedAt: rp.ModifiedAt, ViewedAt: rp.ViewedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": out})
}

func (h *handlers) onenotePrefsGet(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	p, err := svc.GetPrefs(r.Context(), uid)
	if err != nil {
		h.onenoteError(w, "get prefs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"defaultNotebookId": p.DefaultNotebookID,
		"defaultSectionId":  p.DefaultSectionID,
		"lastSyncedAt":      p.LastSyncedAt,
	})
}

// --- write handlers ------------------------------------------------------

func (h *handlers) onenotePrefsPut(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	var req struct {
		DefaultNotebookID string `json:"defaultNotebookId"`
		DefaultSectionID  string `json:"defaultSectionId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if err := svc.SetDefaults(r.Context(), uid, req.DefaultNotebookID, req.DefaultSectionID); err != nil {
		h.onenoteError(w, "set prefs", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) onenotePageCreate(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	sectionID := chi.URLParam(r, "id")
	var req struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	page, err := svc.CreatePage(r.Context(), uid, sectionID, onenote.NewPage{Title: req.Title, Markdown: req.Markdown})
	if err != nil {
		h.onenoteError(w, "create page", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"page": toPageDTO(page)})
}

func (h *handlers) onenotePageAppend(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	pageID := chi.URLParam(r, "id")
	var req struct {
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Markdown) == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "markdown is required")
		return
	}
	if err := svc.AppendBlock(r.Context(), uid, pageID, req.Markdown); err != nil {
		h.onenoteError(w, "append block", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) onenoteElementReplace(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	pageID := chi.URLParam(r, "id")
	elementID := chi.URLParam(r, "elementId")
	var req struct {
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if err := svc.ReplaceElement(r.Context(), uid, pageID, elementID, req.Markdown); err != nil {
		h.onenoteError(w, "replace element", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) onenoteBodyReplace(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	pageID := chi.URLParam(r, "id")
	var req struct {
		Markdown       string `json:"markdown"`
		ConfirmReplace bool   `json:"confirmReplace"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	page, err := svc.ReplaceBody(r.Context(), uid, pageID, req.Markdown, req.ConfirmReplace)
	if err != nil {
		h.onenoteError(w, "replace body", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": toPageDTO(page)})
}

func (h *handlers) onenotePageDelete(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := h.onenoteGuard(w, r)
	if !ok {
		return
	}
	pageID := chi.URLParam(r, "id")
	if err := svc.DeletePage(r.Context(), uid, r.URL.Query().Get("sectionId"), pageID); err != nil {
		h.onenoteError(w, "delete page", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
