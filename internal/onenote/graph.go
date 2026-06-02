package onenote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// graph.go holds the Microsoft Graph wire shapes (unexported), the mapping to
// our domain types, and the low-level authenticated HTTP plumbing every
// Service method funnels through.

const (
	graphBaseURL = "https://graph.microsoft.com/v1.0"
	// scopeNotesReadWrite is the delegated scope every OneNote call requires.
	// usertoken.Source checks it against the stored grant and returns
	// ErrMissingScope when an older link predates it.
	scopeNotesReadWrite = "Notes.ReadWrite"
	// maxRespBytes caps how much of a Graph response we read. Page content can
	// be large (embedded base64 images), but 16 MiB is a generous ceiling that
	// still bounds a hostile/buggy response.
	maxRespBytes = 16 << 20
)

// GraphError is returned when Graph responds with a non-2xx status. StatusCode
// drives the REST layer's mapping (4xx → 400, 5xx → 502); Code/Message carry
// Graph's structured error for logs and detail.
type GraphError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *GraphError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("graph %d: %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("graph %d: %s", e.StatusCode, e.Message)
}

// --- wire shapes ---------------------------------------------------------

type graphList[T any] struct {
	Value []T `json:"value"`
}

type graphLinks struct {
	OneNoteWebURL    graphHref `json:"oneNoteWebUrl"`
	OneNoteClientURL graphHref `json:"oneNoteClientUrl"`
}

type graphHref struct {
	Href string `json:"href"`
}

type graphNotebook struct {
	ID                   string     `json:"id"`
	DisplayName          string     `json:"displayName"`
	IsDefault            bool       `json:"isDefault"`
	Color                string     `json:"color"` // hex (e.g. "#A6A6A6") or "none"/""
	CreatedDateTime      string     `json:"createdDateTime"`
	LastModifiedDateTime string     `json:"lastModifiedDateTime"`
	Links                graphLinks `json:"links"`
}

type graphSection struct {
	ID                   string `json:"id"`
	DisplayName          string `json:"displayName"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	ParentNotebook       struct {
		ID string `json:"id"`
	} `json:"parentNotebook"`
}

type graphPage struct {
	ID                   string     `json:"id"`
	Title                string     `json:"title"`
	CreatedDateTime      string     `json:"createdDateTime"`
	LastModifiedDateTime string     `json:"lastModifiedDateTime"`
	Links                graphLinks `json:"links"`
	ParentSection        struct {
		ID string `json:"id"`
	} `json:"parentSection"`
}

// --- mapping -------------------------------------------------------------

func (g graphNotebook) toDomain() Notebook {
	color := g.Color
	if color == "none" {
		color = "" // Graph's sentinel for "no color" → treat as unset
	}
	return Notebook{
		ID:         g.ID,
		Name:       g.DisplayName,
		IsDefault:  g.IsDefault,
		Color:      color,
		CreatedAt:  parseGraphTime(g.CreatedDateTime),
		ModifiedAt: parseGraphTime(g.LastModifiedDateTime),
		WebURL:     g.Links.OneNoteWebURL.Href,
	}
}

func (g graphSection) toDomain() Section {
	return Section{
		ID:         g.ID,
		Name:       g.DisplayName,
		NotebookID: g.ParentNotebook.ID,
		CreatedAt:  parseGraphTime(g.CreatedDateTime),
		ModifiedAt: parseGraphTime(g.LastModifiedDateTime),
	}
}

func (g graphPage) toDomain() Page {
	return Page{
		ID:         g.ID,
		Title:      g.Title,
		SectionID:  g.ParentSection.ID,
		CreatedAt:  parseGraphTime(g.CreatedDateTime),
		ModifiedAt: parseGraphTime(g.LastModifiedDateTime),
		WebURL:     g.Links.OneNoteWebURL.Href,
		ClientURL:  g.Links.OneNoteClientURL.Href,
	}
}

// parseGraphTime parses Graph's RFC3339 timestamps; returns the zero time on
// any parse failure (callers treat zero as "unknown").
func parseGraphTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// --- HTTP plumbing -------------------------------------------------------

// do performs one authenticated Graph request. It resolves a valid access
// token for userID (refreshing on demand via the token source), issues the
// request against baseURL+path, and returns the response body for 2xx or a
// *GraphError otherwise. A non-nil body is sent with the given contentType.
func (s *Service) do(ctx context.Context, userID int64, method, path, contentType string, body []byte) ([]byte, error) {
	return s.doURL(ctx, userID, method, s.baseURL+path, contentType, body)
}

// doURL is the request core: it resolves a token for userID, issues method
// against an already-fully-qualified url, and returns the 2xx body or a
// *GraphError. do() prepends baseURL to a relative path; FetchResource passes
// an absolute Graph resource URL straight through. The Accept header asks for
// JSON, which Graph ignores for binary resource ($value) and HTML (/content)
// endpoints — both return their native content-type regardless.
func (s *Service) doURL(ctx context.Context, userID int64, method, fullURL, contentType string, body []byte) ([]byte, error) {
	token, err := s.tokens.Token(ctx, userID, scopeNotesReadWrite)
	if err != nil {
		return nil, err // typed usertoken error — handler maps it
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if err != nil {
		return nil, fmt.Errorf("onenote: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := s.httpDo(req)
	if err != nil {
		return nil, fmt.Errorf("onenote: graph request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, fmt.Errorf("onenote: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return respBody, parseGraphError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// parseGraphError extracts Graph's structured {"error":{"code","message"}}
// payload into a *GraphError, falling back to the raw body as the message.
func parseGraphError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	ge := &GraphError{StatusCode: status}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Code != "" {
		ge.Code = payload.Error.Code
		ge.Message = payload.Error.Message
	} else {
		msg := string(body)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		ge.Message = msg
	}
	return ge
}

// escapeID URL-escapes a Graph object id for use as a single path segment.
// Graph onenote ids are mostly URL-safe but can contain characters (e.g. '!')
// that must be escaped to avoid malformed paths.
func escapeID(id string) string {
	return url.PathEscape(id)
}
