package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// HackerNews fetches stories from the public Firebase-hosted HN API. Free,
// keyless, occasionally slow — the per-item fan-out keeps total wall time
// well under a second for the default top-30 even on home connections.
type HackerNews struct {
	HTTPClient *http.Client
	// MaxConcurrency caps the concurrent /item fetches. HN tolerates
	// dozens easily but we keep it modest to be a good citizen.
	MaxConcurrency int
}

// NewHackerNews returns a sensibly configured HN provider. Timeout covers the
// full top-stories + per-item fan-out (worst case ~30 sequential roundtrips
// when concurrency is 1).
func NewHackerNews() *HackerNews {
	return &HackerNews{
		HTTPClient:     &http.Client{Timeout: 10 * time.Second},
		MaxConcurrency: 8,
	}
}

const hnBaseURL = "https://hacker-news.firebaseio.com/v0"

// hnItem is the wire-format payload from /item/<id>.json. We accept a loose
// subset — HN sends extra fields like `descendants`, `parent`, etc., that
// we ignore.
type hnItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	By    string `json:"by"`
	Score int    `json:"score"`
	Time  int64  `json:"time"`
	Kids  []int  `json:"kids"`
	Type  string `json:"type"`
	Dead  bool   `json:"dead"`
}

// TopStories fetches the current ranked story list and resolves each entry.
// Order in the returned slice matches the HN rank exactly.
func (p *HackerNews) TopStories(ctx context.Context, limit int) ([]Story, error) {
	if limit <= 0 {
		limit = 30
	}
	ids, err := p.fetchTopIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("hn: top stories: %w", err)
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}

	// Fetch items concurrently with a bounded worker pool. A "found"
	// counter is unnecessary because we keep results in a fixed-position
	// slice keyed by the request order — fast and simple.
	type slot struct {
		idx   int
		story Story
		err   error
	}
	out := make([]Story, len(ids))
	results := make(chan slot, len(ids))

	maxC := p.MaxConcurrency
	if maxC <= 0 {
		maxC = 4
	}
	sem := make(chan struct{}, maxC)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			item, err := p.fetchItem(ctx, id)
			if err != nil {
				results <- slot{idx: i, err: err}
				return
			}
			results <- slot{idx: i, story: itemToStory(item)}
		}(i, id)
	}
	go func() { wg.Wait(); close(results) }()
	for s := range results {
		if s.err != nil {
			// Single item failures don't fail the whole load — they leave
			// an empty slot which we filter below.
			continue
		}
		out[s.idx] = s.story
	}
	// Compact out empty slots from failed/dead items.
	filtered := out[:0]
	for _, s := range out {
		if s.ID != 0 {
			filtered = append(filtered, s)
		}
	}
	return filtered, nil
}

func (p *HackerNews) fetchTopIDs(ctx context.Context) ([]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hnBaseURL+"/topstories.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	var ids []int64
	if err := json.NewDecoder(resp.Body).Decode(&ids); err != nil {
		return nil, fmt.Errorf("decode top: %w", err)
	}
	return ids, nil
}

func (p *HackerNews) fetchItem(ctx context.Context, id int64) (hnItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/item/%d.json", hnBaseURL, id), nil)
	if err != nil {
		return hnItem{}, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return hnItem{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return hnItem{}, fmt.Errorf("item %d status %d", id, resp.StatusCode)
	}
	var item hnItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return hnItem{}, fmt.Errorf("decode item %d: %w", id, err)
	}
	if item.Dead {
		return hnItem{}, fmt.Errorf("item %d is dead", id)
	}
	return item, nil
}

func itemToStory(i hnItem) Story {
	return Story{
		ID:      i.ID,
		Title:   i.Title,
		URL:     i.URL,
		Author:  i.By,
		Score:   i.Score,
		Posted:  time.Unix(i.Time, 0).UTC(),
		KidsCnt: len(i.Kids),
	}
}
