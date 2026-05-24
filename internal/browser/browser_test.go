package browser

import (
	"testing"

	"github.com/nickna/ssh.night.ms/internal/reader"
)

func TestIsLikelyURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"https://example.com", true},
		{"HTTP://example.com/x?y=1", true},
		{"example.com", true},
		{"sub.example.co.uk/path", true},
		{"go.dev", true},
		{"localhost", false}, // no dot — treat as search query
		{"hello world", false},
		{"?go.dev", false}, // explicit force-search prefix
		{"some random query", false},
		{"127.0.0.1/admin", true},
	}
	for _, tc := range cases {
		if got := IsLikelyURL(tc.in); got != tc.want {
			t.Errorf("IsLikelyURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  https://x.com ", "https://x.com"},
		{"http://x.com", "http://x.com"},
		{"example.com", "https://example.com"},
		{"  example.com/path  ", "https://example.com/path"},
	}
	for _, tc := range cases {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHistory(t *testing.T) {
	h := New()
	if h.CanBack() || h.CanForward() {
		t.Errorf("fresh history should allow neither back nor forward")
	}
	h.Push(Entry{URL: "a"})
	h.Push(Entry{URL: "b"})
	h.Push(Entry{URL: "c"})
	if cur, total := h.Position(); cur != 3 || total != 3 {
		t.Errorf("position = %d/%d, want 3/3", cur, total)
	}
	got, ok := h.Back()
	if !ok || got.URL != "b" {
		t.Errorf("Back = %+v ok=%v, want b ok=true", got, ok)
	}
	got, ok = h.Back()
	if !ok || got.URL != "a" {
		t.Errorf("Back = %+v ok=%v, want a ok=true", got, ok)
	}
	if _, ok := h.Back(); ok {
		t.Errorf("Back past start should fail")
	}
	got, ok = h.Forward()
	if !ok || got.URL != "b" {
		t.Errorf("Forward = %+v ok=%v, want b ok=true", got, ok)
	}
	// Push from middle truncates forward stack.
	h.Push(Entry{URL: "z"})
	if _, ok := h.Forward(); ok {
		t.Errorf("Forward after Push from middle should fail (forward stack truncated)")
	}
	if cur, total := h.Position(); cur != 3 || total != 3 {
		t.Errorf("position after mid-push = %d/%d, want 3/3 (a,b,z)", cur, total)
	}
}

func TestHistoryDedupeReload(t *testing.T) {
	h := New()
	h.Push(Entry{URL: "a", Title: "First"})
	h.Push(Entry{URL: "a", Title: "Updated"})
	if _, total := h.Position(); total != 1 {
		t.Errorf("duplicate consecutive Push should not grow stack, total = %d", total)
	}
	cur, _ := h.Current()
	if cur.Title != "Updated" {
		t.Errorf("title should be refreshed to %q, got %q", "Updated", cur.Title)
	}
}

func TestHistoryCap(t *testing.T) {
	h := New()
	for i := 0; i < historyCap+25; i++ {
		h.Push(Entry{URL: string(rune('a' + (i % 26))) + string(rune('0'+i%10)) + string(rune(i))})
	}
	if _, total := h.Position(); total != historyCap {
		t.Errorf("history total = %d, want capped at %d", total, historyCap)
	}
}

func TestCacheLRU(t *testing.T) {
	c := NewCache()
	for i := 0; i < cacheCap+3; i++ {
		c.Put(string(rune('a'+i)), &reader.Article{URL: string(rune('a' + i))})
	}
	// First three should be evicted (oldest).
	for i := 0; i < 3; i++ {
		if _, ok := c.Get(string(rune('a' + i))); ok {
			t.Errorf("entry %q should have been evicted", string(rune('a'+i)))
		}
	}
	// Last entry should still be there.
	last := string(rune('a' + cacheCap + 2))
	if _, ok := c.Get(last); !ok {
		t.Errorf("most recent entry %q missing", last)
	}
}

func TestCacheForget(t *testing.T) {
	c := NewCache()
	c.Put("a", &reader.Article{URL: "a"})
	c.Forget("a")
	if _, ok := c.Get("a"); ok {
		t.Errorf("Forget did not evict entry")
	}
}

func TestCacheBumpsOnGet(t *testing.T) {
	c := NewCache()
	// Fill cache exactly.
	for i := 0; i < cacheCap; i++ {
		c.Put(string(rune('a'+i)), &reader.Article{})
	}
	// Touch the oldest so it becomes most-recent.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("entry a should still be present")
	}
	// Now insert one more — the second-oldest should be evicted, not 'a'.
	c.Put("new", &reader.Article{})
	if _, ok := c.Get("a"); !ok {
		t.Errorf("entry a was evicted despite recent Get")
	}
	if _, ok := c.Get(string(rune('a' + 1))); ok {
		t.Errorf("entry b should have been evicted as the new oldest")
	}
}
