// Package bookmarks is the thin domain wrapper around sqlc-generated bookmark
// queries. Keeps *gen.Queries and pgtype.Timestamptz out of the Browser
// screen so the UI talks in plain Go types.
package bookmarks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// maxList is the upper bound passed to ListBookmarks. The schema doesn't
// cap rows; this is a UI safety net so a power user with 10k bookmarks
// doesn't paint every one into the terminal at once.
const maxList = 200

// Bookmark is the rendered-ready record. Mirrors the columns 1:1 minus the
// pgtype wrappers.
type Bookmark struct {
	ID        int64
	URL       string
	Title     string
	SortOrder int32
	CreatedAt time.Time
}

// Service is the per-process bookmark service. Holds a reference to the
// sqlc-generated *Queries; cheap to construct, safe for concurrent use as
// long as the underlying pgxpool is.
type Service struct {
	q *gen.Queries
}

// New builds a Service. q may be nil — Add/List/Delete all return a clear
// error in that case rather than panicking, so the Browser screen can be
// constructed without the DB being wired (e.g. local smoke tests).
func New(q *gen.Queries) *Service { return &Service{q: q} }

// ErrNotConfigured surfaces when Service was constructed without queries.
var ErrNotConfigured = errors.New("bookmarks: service not configured")

// Add inserts (or refreshes the title of) a bookmark for userID. Returns
// the canonical record so the caller can confirm what was stored.
func (s *Service) Add(ctx context.Context, userID int64, url, title string) (Bookmark, error) {
	if s == nil || s.q == nil {
		return Bookmark{}, ErrNotConfigured
	}
	if userID == 0 {
		return Bookmark{}, errors.New("bookmarks: missing user id")
	}
	if url == "" {
		return Bookmark{}, errors.New("bookmarks: empty url")
	}
	if title == "" {
		title = url
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	row, err := s.q.AddBookmark(ctx, gen.AddBookmarkParams{
		UserID:    userID,
		Url:       url,
		Title:     title,
		CreatedAt: now,
	})
	if err != nil {
		return Bookmark{}, fmt.Errorf("bookmarks: add: %w", err)
	}
	return Bookmark{
		ID:        row.ID,
		URL:       row.Url,
		Title:     row.Title,
		SortOrder: row.SortOrder,
		CreatedAt: row.CreatedAt.Time,
	}, nil
}

// List returns the user's bookmarks, oldest sort_order first.
func (s *Service) List(ctx context.Context, userID int64) ([]Bookmark, error) {
	if s == nil || s.q == nil {
		return nil, ErrNotConfigured
	}
	if userID == 0 {
		return nil, errors.New("bookmarks: missing user id")
	}
	rows, err := s.q.ListBookmarks(ctx, gen.ListBookmarksParams{
		UserID: userID,
		Limit:  maxList,
	})
	if err != nil {
		return nil, fmt.Errorf("bookmarks: list: %w", err)
	}
	out := make([]Bookmark, 0, len(rows))
	for _, r := range rows {
		out = append(out, Bookmark{
			ID:        r.ID,
			URL:       r.Url,
			Title:     r.Title,
			SortOrder: r.SortOrder,
			CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

// Delete removes a single bookmark, scoped to userID so one user can't
// delete another's row even with a guessed id.
func (s *Service) Delete(ctx context.Context, userID, id int64) error {
	if s == nil || s.q == nil {
		return ErrNotConfigured
	}
	if userID == 0 || id == 0 {
		return errors.New("bookmarks: missing id or user id")
	}
	if err := s.q.DeleteBookmark(ctx, gen.DeleteBookmarkParams{ID: id, UserID: userID}); err != nil {
		return fmt.Errorf("bookmarks: delete: %w", err)
	}
	return nil
}
