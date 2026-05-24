package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// publicProfilePage renders a public-facing /u/{handle} page. Anyone can see
// it; signed-in users get a header that ties back to their own session.
type publicProfilePage struct {
	pageData
	Handle   string
	IsSysop  bool
	JoinedAt string
}

func (h *handlers) publicProfile(w http.ResponseWriter, r *http.Request) {
	handle := chi.URLParam(r, "handle")
	if handle == "" {
		http.NotFound(w, r)
		return
	}
	user, err := h.deps.Queries.GetUserByHandle(r.Context(), handle)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("public profile: load", "handle", handle, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := publicProfilePage{
		pageData: h.basePage(r, "@"+user.Handle),
		Handle:   user.Handle,
		IsSysop:  user.IsSysop,
		JoinedAt: user.CreatedAt.Time.Format("January 2006"),
	}
	h.renderProfile(w, "public_profile", data)
}

// avatar serves the avatar PNG for a handle. Order of precedence:
//   1. Uploaded profile picture (NIGHTMS_PFP_DIR/<user_id>.png) when
//      users.profile_picture_updated_at is set. ETag = upload timestamp so
//      a new upload always invalidates browser caches.
//   2. Otherwise the deterministic identicon. ETag = hash of handle|size.
//
// In both cases we serve Cache-Control max-age=86400. Handle rename
// keeps the same avatar URL — a renamed user's old avatar can therefore
// linger in caches for up to a day.
func (h *handlers) avatar(w http.ResponseWriter, r *http.Request) {
	handle := chi.URLParam(r, "handle")
	if handle == "" {
		http.NotFound(w, r)
		return
	}

	user, err := h.deps.Queries.GetUserByHandle(r.Context(), handle)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		h.deps.Logger.Error("avatar: load", "handle", handle, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Uploaded picture wins if the column is stamped AND the file exists.
	if path, updatedAt, ok := h.readProfilePicture(r.Context(), user); ok {
		etag := `"upload-` + strconv.FormatInt(updatedAt.Unix(), 36) + `"`
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("ETag", etag)
		http.ServeFile(w, r, path)
		return
	}

	// Identicon fallback.
	size := 64
	if s := r.URL.Query().Get("size"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			if n < 16 {
				n = 16
			}
			if n > 512 {
				n = 512
			}
			size = n
		}
	}
	png, err := GenerateIdenticon(handle, size)
	if err != nil {
		h.deps.Logger.Error("avatar: generate", "handle", handle, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	etag := identiconETag(handle, size)
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	_, _ = w.Write(png)
}

// identiconETag returns a stable quoted-string ETag for the (handle, size)
// pair. We hash to obscure how the cache key is formed; not security, just
// neatness.
func identiconETag(handle string, size int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", strings.ToLower(handle), size)))
	return `"` + hex.EncodeToString(h[:8]) + `"`
}
