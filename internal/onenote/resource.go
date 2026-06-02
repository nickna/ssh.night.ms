package onenote

import (
	"context"
	"net/http"
	"strings"
)

// FetchResource downloads a OneNote page resource (an embedded image's
// src/data-fullres-src, which points at an auth-protected Graph endpoint like
// .../onenote/resources/{id}/$value) and returns its raw bytes. It exists
// because the shared sess.Images pool fetches anonymously and can't carry the
// user's bearer token — these URLs 401 without it.
//
// rawURL is normally an absolute Graph URL captured from the includeIDs page
// HTML; a relative reference is resolved against the service base URL. The
// caller decodes the bytes and renders them (or falls back to a placeholder on
// any error), so a fetch failure is never fatal to a page read.
func (s *Service) FetchResource(ctx context.Context, userID int64, rawURL string) ([]byte, error) {
	url := rawURL
	if !strings.Contains(url, "://") {
		url = s.baseURL + ensureLeadingSlash(url)
	}
	return s.doURL(ctx, userID, http.MethodGet, url, "", nil)
}

func ensureLeadingSlash(p string) string {
	if p == "" || strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}
