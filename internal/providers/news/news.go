// Package news defines the news provider contract + a HackerNews
// implementation. Lives under internal/providers/ alongside the weather
// and finance providers. Providers are deliberately simple value-returning
// interfaces so screens never need to know whose API they're talking to.
package news

import (
	"context"
	"net/url"
	"time"
)

// Story is the rendered-ready shape passed to the TUI. Provider implementations
// normalize whatever the upstream API returns into this struct.
//
// SourceID identifies which Source produced the row. Per-source providers
// stamp it on output so a future merged-feed view can route a click back to
// the right provider; the current tabs-per-source UI never reads it.
//
// URL is required: every provider fills it with a navigable link. For
// discussion-only items (Ask HN, Lobsters self-posts) the provider
// substitutes its own comment-page URL so the screen sees a single uniform
// "URL always populated" invariant.
type Story struct {
	SourceID string
	ID       int64
	Title    string
	URL      string
	Author   string
	Score    int
	Posted   time.Time
	KidsCnt  int // top-level comment count
}

// Host returns the bare hostname of URL, or "" when URL is empty. Useful
// for the list view ("example.com") without exposing the whole link.
func (s Story) Host() string {
	if s.URL == "" {
		return ""
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Provider is the contract every news source implements. A Provider returns
// up to limit stories ordered by upstream rank (newest-first or score-first
// depending on source). Caching is the caller's job — wrap a real Provider
// in NewCache when you want TTL'd reads.
type Provider interface {
	TopStories(ctx context.Context, limit int) ([]Story, error)
}
