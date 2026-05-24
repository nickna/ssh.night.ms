package search

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const ddgFixture = `
<div class="results">
  <div class="result">
    <div class="result__body">
      <h2 class="result__title"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F">The Go Programming Language</a></h2>
      <a class="result__url" href="https://go.dev/">go.dev</a>
      <a class="result__snippet" href="https://go.dev/">Go is an open source programming language that makes it easy to build simple, reliable, and efficient software.</a>
    </div>
  </div>
  <div class="result">
    <div class="result__body">
      <h2 class="result__title"><a class="result__a" href="https://golang.org/">Golang Home</a></h2>
      <div class="result__snippet">Redirect home page.</div>
    </div>
  </div>
</div>
`

func TestExtractDDGResults(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(ddgFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := extractDDGResults(doc, 10)
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(got), got)
	}
	if got[0].Title != "The Go Programming Language" {
		t.Errorf("title[0] = %q, want %q", got[0].Title, "The Go Programming Language")
	}
	if got[0].URL != "https://go.dev/" {
		t.Errorf("url[0] = %q, want unwrapped go.dev", got[0].URL)
	}
	if !strings.Contains(got[0].Snippet, "open source programming language") {
		t.Errorf("snippet[0] missing expected text: %q", got[0].Snippet)
	}
	if got[1].Title != "Golang Home" {
		t.Errorf("title[1] = %q, want %q", got[1].Title, "Golang Home")
	}
	if got[1].URL != "https://golang.org/" {
		t.Errorf("url[1] = %q, want direct href", got[1].URL)
	}
	if got[1].Snippet != "Redirect home page." {
		t.Errorf("snippet[1] = %q, want div fallback", got[1].Snippet)
	}
}

func TestUnwrapDDGRedirect(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://go.dev/", "https://go.dev/"},
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F", "https://go.dev/"},
		{"https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath%3Fq%3D1", "https://example.com/path?q=1"},
		{"/bang?q=!w%20test", "/bang?q=!w%20test"},
	}
	for _, tc := range cases {
		if got := unwrapDDGRedirect(tc.in); got != tc.want {
			t.Errorf("unwrap(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractDDGResultsRespectsLimit(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(ddgFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := extractDDGResults(doc, 1)
	if len(got) != 1 {
		t.Errorf("limit=1 should yield 1 result, got %d", len(got))
	}
}
