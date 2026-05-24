package finance

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Headline is a single finance-news item rendered in the bottom pane of the
// Finance screen or the Detail screen's "related news" section.
type Headline struct {
	Title     string
	URL       string
	Published time.Time
}

// NewsProvider returns finance headlines for a set of tickers. Empty tickers
// triggers the broad-market fallback (Dow / S&P / Nasdaq) so the pane is
// never empty.
type NewsProvider interface {
	ForTickers(ctx context.Context, tickers []string, max int) ([]Headline, error)
}

// YahooRSSNews fetches headlines from finance.yahoo.com/rss/headline. RSS 2.0,
// no key. Per-(ticker-set, max) entries cached for 5 minutes so two users with
// the same watchlist share a fetch.
type YahooRSSNews struct {
	HTTPClient *http.Client
	TTL        time.Duration

	mu    sync.Mutex
	cache map[string]*rssCacheEntry
}

func NewYahooRSSNews() *YahooRSSNews {
	return &YahooRSSNews{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		TTL:        5 * time.Minute,
		cache:      map[string]*rssCacheEntry{},
	}
}

type rssCacheEntry struct {
	mu      sync.Mutex
	items   []Headline
	fetched time.Time
}

var fallbackSymbols = []string{"^DJI", "^GSPC", "^IXIC"}

func normalizeNewsSymbols(in []string) []string {
	if len(in) == 0 {
		return fallbackSymbols
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return fallbackSymbols
	}
	sort.Strings(out)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func (y *YahooRSSNews) ForTickers(ctx context.Context, tickers []string, max int) ([]Headline, error) {
	if max < 1 {
		max = 1
	}
	if max > 30 {
		max = 30
	}
	symbols := normalizeNewsSymbols(tickers)
	key := fmt.Sprintf("%s|%d", strings.Join(symbols, ","), max)

	y.mu.Lock()
	e, ok := y.cache[key]
	if !ok {
		e = &rssCacheEntry{}
		y.cache[key] = e
	}
	y.mu.Unlock()

	e.mu.Lock()
	if e.items != nil && time.Since(e.fetched) < y.TTL {
		out := e.items
		e.mu.Unlock()
		return out, nil
	}
	e.mu.Unlock()

	items, err := y.fetch(ctx, symbols, max)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		if e.items != nil {
			return e.items, nil
		}
		return nil, err
	}
	e.items = items
	e.fetched = time.Now()
	return items, nil
}

func (y *YahooRSSNews) fetch(ctx context.Context, symbols []string, max int) ([]Headline, error) {
	u := "https://finance.yahoo.com/rss/headline?s=" + url.QueryEscape(strings.Join(symbols, ","))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; nightms-bbs/1.0; +https://night.ms)")
	resp, err := y.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yahoo rss: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("yahoo rss: status %d", resp.StatusCode)
	}
	var doc struct {
		Channel struct {
			Items []struct {
				Title   string `xml:"title"`
				Link    string `xml:"link"`
				PubDate string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("yahoo rss: decode: %w", err)
	}
	out := make([]Headline, 0, len(doc.Channel.Items))
	for _, it := range doc.Channel.Items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		out = append(out, Headline{
			Title:     title,
			URL:       strings.TrimSpace(it.Link),
			Published: parseRSSDate(it.PubDate),
		})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

// parseRSSDate handles RFC 822 ("Wed, 14 May 2026 09:12:00 -0400") and a few
// common variants. Unparseable inputs fall back to "now" so the headline still
// sorts in a sensible position.
func parseRSSDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		time.RFC822Z,
		time.RFC822,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

var _ NewsProvider = (*YahooRSSNews)(nil)
