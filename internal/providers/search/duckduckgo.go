package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// duckDuckGoEndpoint is DDG's plain-HTML SERP. No API key, scrape-friendly,
// stable enough for our purposes. The /html/ prefix is the path that opts
// out of the JS-heavy SPA results page.
const duckDuckGoEndpoint = "https://html.duckduckgo.com/html/"

// DuckDuckGo is a Provider that scrapes the DDG HTML results page. Cheap,
// no key, no rate-limit accounting beyond what DDG enforces upstream.
type DuckDuckGo struct {
	Client *http.Client
}

// NewDuckDuckGo builds a DDG provider with the given HTTP client, or a
// sensible default when nil.
func NewDuckDuckGo(client *http.Client) *DuckDuckGo {
	if client == nil {
		client = http.DefaultClient
	}
	return &DuckDuckGo{Client: client}
}

// Search submits the query and returns up to `limit` results. Markup drift
// at DDG would surface as zero results — the caller should display a
// friendly "no results" notice rather than treating it as a hard failure.
func (d *DuckDuckGo) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	if d == nil {
		return nil, fmt.Errorf("search: nil provider")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("search: empty query")
	}
	if limit <= 0 {
		limit = 20
	}
	form := url.Values{}
	form.Set("q", q)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, duckDuckGoEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("search: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; nightms-browser/1)")
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search: http %d", resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("search: parse: %w", err)
	}
	results := extractDDGResults(doc, limit)
	return results, nil
}

// extractDDGResults walks the parsed doc and collects results. DDG's HTML
// page nests each result inside a div with class "result__body" containing
// an <a class="result__a"> (title + href) and an <a class="result__snippet">.
// Some snippets use <a class="result__snippet"> while others are <div
// class="result__snippet"> — handle both.
func extractDDGResults(root *html.Node, limit int) []Result {
	var out []Result
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil || len(out) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__body") {
			r := parseDDGResult(n)
			if r.Title != "" && r.URL != "" {
				out = append(out, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func parseDDGResult(node *html.Node) Result {
	var r Result
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			switch {
			case hasClass(n, "result__a") && r.Title == "":
				r.Title = textContent(n)
				if href := attr(n, "href"); href != "" {
					r.URL = unwrapDDGRedirect(href)
				}
			case hasClass(n, "result__snippet") && r.Snippet == "":
				r.Snippet = textContent(n)
			}
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__snippet") && r.Snippet == "" {
			r.Snippet = textContent(n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	return r
}

// unwrapDDGRedirect undoes DDG's "/l/?kh=...&uddg=<percent-encoded>" wrapper
// so the screen navigates to the real destination. When the href doesn't
// match the wrapper shape, it's returned as-is — works for already-direct
// links and the bang shortcuts (!w, !so, etc.).
func unwrapDDGRedirect(href string) string {
	// DDG sometimes emits protocol-relative redirector URLs ("//duckduckgo.com/l/?...").
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if !strings.HasSuffix(u.Path, "/l/") {
		return href
	}
	if real := u.Query().Get("uddg"); real != "" {
		if decoded, err := url.QueryUnescape(real); err == nil {
			return decoded
		}
		return real
	}
	return href
}

func hasClass(n *html.Node, want string) bool {
	if n == nil {
		return false
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "class") {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
			return false
		}
	}
	return false
}

func attr(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
