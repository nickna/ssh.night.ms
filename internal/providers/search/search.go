// Package search exposes a single Provider interface the Browser screen uses
// to turn non-URL input from the URL bar into result lists. Implementations
// live alongside (duckduckgo.go etc.) and are constructed in main.go.
package search

import "context"

// Result is one returned search hit, mapped through the Browser screen's
// synthetic-article renderer (title=heading, snippet=paragraph, url=link).
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Provider is the search engine adapter. Implementations should respect the
// caller's context for cancellation and timeouts.
type Provider interface {
	Search(ctx context.Context, query string, limit int) ([]Result, error)
}
