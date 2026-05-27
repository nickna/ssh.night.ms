package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/gorilla/csrf"
	"github.com/jackc/pgx/v5"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

type handlers struct {
	deps      Deps
	cfg       Config
	templates map[string]*template.Template
	sessions  *sessionStore
	oauth     OAuthProviders
}

// identityKey is the request-context key under which attachIdentity stashes
// the resolved User row (when a valid session cookie is present).
type identityKey struct{}

// pageData is the value handed to every template render. Identity is nil for
// signed-out users. CSRFField renders the hidden form input gorilla/csrf
// validates on POST.
type pageData struct {
	Title     string
	Host      string
	Identity  *webIdentity
	CSRFField template.HTML
	Error     string

	// Flash is a one-shot status surface used by OAuth-driven redirects.
	FlashKind string
	FlashText string

	// Connections is the connected-accounts sub-page model. Non-nil only on
	// /profile/connections.
	Connections *connectionsData

	// Rename sub-page model.
	Rename *renameData
}

type webIdentity struct {
	UserID  int64
	Handle  string
	IsSysop bool
}

func (h *handlers) attachIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := h.sessions.Read(r.Context(), r)
		if err != nil {
			// Missing / malformed / expired / Redis blip — proceed
			// unauthenticated rather than locking the user out.
			next.ServeHTTP(w, r)
			return
		}
		user, err := h.deps.Queries.GetUserByID(r.Context(), userID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				h.deps.Logger.Warn("web: load identity", "user_id", userID, "err", err)
			}
			// Stale session pointing at a deleted user — clear cookie + continue.
			h.sessions.Clear(r.Context(), r, w)
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey{}, &webIdentity{
			UserID:  user.ID,
			Handle:  user.Handle,
			IsSysop: user.IsSysop,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func identityFrom(r *http.Request) *webIdentity {
	v, _ := r.Context().Value(identityKey{}).(*webIdentity)
	return v
}

func (h *handlers) basePage(r *http.Request, title string) pageData {
	page := pageData{
		Title:     title,
		Host:      h.cfg.PublicHost,
		Identity:  identityFrom(r),
		CSRFField: csrf.TemplateField(r),
	}
	return page
}

func (h *handlers) render(w http.ResponseWriter, page string, data pageData) {
	tpl, ok := h.templates[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.deps.Logger.Error("web: render", "page", page, "err", err)
	}
}

func (h *handlers) landing(w http.ResponseWriter, r *http.Request) {
	h.render(w, "landing", h.basePage(r, "home"))
}

func (h *handlers) loginGet(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, "login", h.basePage(r, "log in"))
}

func (h *handlers) loginPost(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	handle := strings.TrimSpace(r.PostFormValue("handle"))
	password := r.PostFormValue("password")
	if handle == "" || password == "" {
		page := h.basePage(r, "log in")
		page.Error = "handle and password required"
		h.render(w, "login", page)
		return
	}
	// auth.Lookup.ByPassword does all the heavy lifting: rate limiting via
	// the same Redis keys SSH uses, timing-equivalent failure paths, etc.
	decision := h.deps.Lookup.ByPassword(r.Context(), handle, password, remoteAddrNetAddr(r))
	switch d := decision.(type) {
	case auth.Known:
		if _, err := h.sessions.Set(r.Context(), r, w, d.UserID); err != nil {
			h.deps.Logger.Error("web: create session", "user_id", d.UserID, "err", err)
			page := h.basePage(r, "log in")
			page.Error = "could not create session — try again"
			w.WriteHeader(http.StatusInternalServerError)
			h.render(w, "login", page)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case auth.RateLimited:
		page := h.basePage(r, "log in")
		page.Error = "too many attempts — try again later"
		w.WriteHeader(http.StatusTooManyRequests)
		h.render(w, "login", page)
	case auth.Banned:
		page := h.basePage(r, "log in")
		page.Error = "account is banned"
		w.WriteHeader(http.StatusForbidden)
		h.render(w, "login", page)
	default:
		page := h.basePage(r, "log in")
		page.Error = "invalid handle or password"
		w.WriteHeader(http.StatusUnauthorized)
		h.render(w, "login", page)
	}
}

func (h *handlers) logoutPost(w http.ResponseWriter, r *http.Request) {
	h.sessions.Clear(r.Context(), r, w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// logoutAllPost revokes every active session for the current user — the
// "Log out everywhere" button. Anonymous requests redirect to /login; the
// CSRF group covers the POST itself. Always clears the current request's
// cookie too, so the browser that fired this lands on the landing page
// already logged out.
func (h *handlers) logoutAllPost(w http.ResponseWriter, r *http.Request) {
	ident := identityFrom(r)
	if ident == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := h.sessions.ClearAllForUser(r.Context(), ident.UserID); err != nil {
		h.deps.Logger.Error("web: clear all sessions", "user_id", ident.UserID, "err", err)
		http.Error(w, "could not clear sessions", http.StatusInternalServerError)
		return
	}
	h.sessions.Clear(r.Context(), r, w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requireQueries is a small helper for handlers that need typed queries;
// keeps the call site terse.
func (h *handlers) requireQueries() *gen.Queries { return h.deps.Queries }
