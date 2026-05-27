package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLobstersDecodeStringSubmitter(t *testing.T) {
	// Modern lobsters API — submitter_user as a bare string, comments_count
	// (not comment_count), and an external URL.
	body := `[
	  {
	    "short_id": "abc123",
	    "short_id_url": "https://lobste.rs/s/abc123",
	    "created_at": "2026-05-26T12:34:56.000-07:00",
	    "title": "A really good post",
	    "url": "https://example.com/post",
	    "score": 42,
	    "comments_count": 7,
	    "comments_url": "https://lobste.rs/s/abc123/a_really_good_post",
	    "submitter_user": "alice",
	    "tags": ["programming"]
	  },
	  {
	    "short_id": "def456",
	    "created_at": "2026-05-26T11:00:00.000-07:00",
	    "title": "Ask: a self post",
	    "url": "",
	    "score": 12,
	    "comments_count": 3,
	    "comments_url": "https://lobste.rs/s/def456/ask_a_self_post",
	    "submitter_user": "bob"
	  }
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hottest.json" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Error("expected User-Agent to be set")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := NewLobsters()
	p.BaseURL = srv.URL

	got, err := p.TopStories(context.Background(), 30)
	if err != nil {
		t.Fatalf("TopStories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 stories, got %d", len(got))
	}

	first := got[0]
	if first.SourceID != SourceIDLobsters {
		t.Errorf("first SourceID = %q, want %q", first.SourceID, SourceIDLobsters)
	}
	if first.Title != "A really good post" {
		t.Errorf("first Title = %q", first.Title)
	}
	if first.URL != "https://example.com/post" {
		t.Errorf("first URL = %q, want external link", first.URL)
	}
	if first.Author != "alice" {
		t.Errorf("first Author = %q, want alice", first.Author)
	}
	if first.Score != 42 || first.KidsCnt != 7 {
		t.Errorf("first counts = score %d kids %d, want 42/7", first.Score, first.KidsCnt)
	}
	if first.Posted.IsZero() {
		t.Errorf("first Posted should be non-zero")
	}
	if first.Host() != "example.com" {
		t.Errorf("first Host = %q, want example.com", first.Host())
	}

	// Self-post fallback: URL was empty, should be filled with comments_url
	// so the screen sees a uniform Story.URL invariant.
	second := got[1]
	if second.URL != "https://lobste.rs/s/def456/ask_a_self_post" {
		t.Errorf("second URL = %q, want comments_url fallback", second.URL)
	}
}

func TestLobstersDecodeObjectSubmitter(t *testing.T) {
	// Older / forked deployments return submitter_user as an object. The
	// custom UnmarshalJSON must accept both forms.
	body := `[
	  {
	    "short_id": "xyz789",
	    "created_at": "2026-05-26T10:00:00.000Z",
	    "title": "Object-form submitter",
	    "url": "https://example.org/x",
	    "score": 5,
	    "comment_count": 2,
	    "comments_url": "https://lobste.rs/s/xyz789",
	    "submitter_user": {"username": "carol", "karma": 100}
	  }
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := NewLobsters()
	p.BaseURL = srv.URL

	got, err := p.TopStories(context.Background(), 30)
	if err != nil {
		t.Fatalf("TopStories: %v", err)
	}
	if len(got) != 1 || got[0].Author != "carol" {
		t.Fatalf("Author = %q, want carol; got %d stories", func() string {
			if len(got) > 0 {
				return got[0].Author
			}
			return ""
		}(), len(got))
	}
	// comment_count (legacy spelling) should be picked up when comments_count is absent.
	if got[0].KidsCnt != 2 {
		t.Errorf("KidsCnt = %d, want 2 (legacy comment_count)", got[0].KidsCnt)
	}
}

func TestLobstersLimitTrim(t *testing.T) {
	body := `[
	  {"short_id":"a","title":"1","url":"https://x/1","submitter_user":"a"},
	  {"short_id":"b","title":"2","url":"https://x/2","submitter_user":"b"},
	  {"short_id":"c","title":"3","url":"https://x/3","submitter_user":"c"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := NewLobsters()
	p.BaseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := p.TopStories(ctx, 2)
	if err != nil {
		t.Fatalf("TopStories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 stories after trim, got %d", len(got))
	}
}
