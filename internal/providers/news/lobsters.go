package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SourceIDLobsters is the registry ID the Lobsters provider stamps on every
// Story it emits. Mirrors the ID hardcoded in cmd/nightms/main.go::buildProviders.
const SourceIDLobsters = "lobsters"

// Lobsters fetches the front-page ranking from https://lobste.rs. Unlike HN's
// Firebase API there's no per-item fan-out — /hottest.json returns the full
// row per story, so one round-trip is enough for the whole list.
type Lobsters struct {
	HTTPClient *http.Client
	// UserAgent is sent on every request. Lobsters is small and self-hosted;
	// being a polite identified client costs us nothing.
	UserAgent string
	// BaseURL is split out so tests can point at httptest.NewServer. Defaults
	// to https://lobste.rs.
	BaseURL string
}

// NewLobsters returns a sensibly configured Lobsters provider.
func NewLobsters() *Lobsters {
	return &Lobsters{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		UserAgent:  "ssh.night.ms/1.0 (+https://night.ms)",
		BaseURL:    "https://lobste.rs",
	}
}

// lobItem is the wire-format payload from /hottest.json. The struct accepts a
// loose subset — lobsters sends `flags`, `description`, `tags`, etc., that
// we ignore. The two oddities worth a note:
//
//   - SubmitterUser is sometimes a string ("alice") and sometimes an object
//     ({"username":"alice", ...}); UnmarshalJSON below handles both.
//   - The field has historically been spelled both `comments_count` (modern)
//     and `comment_count` (older deployments / forks); we accept both.
type lobItem struct {
	ShortID       string       `json:"short_id"`
	CreatedAt     string       `json:"created_at"`
	Title         string       `json:"title"`
	URL           string       `json:"url"`
	Score         int          `json:"score"`
	CommentsURL   string       `json:"comments_url"`
	CommentsCount int          `json:"comments_count"`
	CommentCount  int          `json:"comment_count"`
	SubmitterUser lobSubmitter `json:"submitter_user"`
}

// lobSubmitter accepts submitter_user as either a bare string or an object
// with a `username` field. Lobsters' modern API returns the string form, but
// fork deployments and older versions return the object — coding for both
// avoids brittleness without forcing a runtime probe.
type lobSubmitter struct {
	Username string
}

func (s *lobSubmitter) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		s.Username = str
		return nil
	}
	var obj struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	s.Username = obj.Username
	return nil
}

// TopStories fetches the current Lobsters front page and trims to limit.
// Order in the returned slice matches the lobste.rs hotness ranking.
func (p *Lobsters) TopStories(ctx context.Context, limit int) ([]Story, error) {
	if limit <= 0 {
		limit = 30
	}
	base := p.BaseURL
	if base == "" {
		base = "https://lobste.rs"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/hottest.json", nil)
	if err != nil {
		return nil, fmt.Errorf("lobsters: build request: %w", err)
	}
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lobsters: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("lobsters: status %d: %s", resp.StatusCode, body)
	}
	var items []lobItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("lobsters: decode: %w", err)
	}
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]Story, 0, len(items))
	for _, it := range items {
		out = append(out, lobItemToStory(it))
	}
	return out, nil
}

func lobItemToStory(i lobItem) Story {
	// Discussion-only posts (no external URL) point at the comments page so
	// every Story.URL is navigable — the News screen never has to branch on
	// "is this a self-post" to render a working link.
	url := i.URL
	if url == "" {
		url = i.CommentsURL
	}
	posted, _ := time.Parse(time.RFC3339, i.CreatedAt) // zero value on parse failure is fine
	kids := i.CommentsCount
	if kids == 0 {
		kids = i.CommentCount // older / forked deployments
	}
	return Story{
		SourceID: SourceIDLobsters,
		ID:       0, // lobsters uses string short_ids; the numeric ID is HN-specific
		Title:    i.Title,
		URL:      url,
		Author:   i.SubmitterUser.Username,
		Score:    i.Score,
		Posted:   posted,
		KidsCnt:  kids,
	}
}
