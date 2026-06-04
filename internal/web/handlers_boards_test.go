package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPageNav(t *testing.T) {
	tests := []struct {
		name      string
		page      int
		perPage   int
		total     int64
		wantPage  int
		wantTotal int
		wantPrev  bool
		wantNext  bool
	}{
		{"empty clamps to one page", 1, 20, 0, 1, 1, false, false},
		{"single full page", 1, 20, 20, 1, 1, false, false},
		{"two pages, first", 1, 20, 21, 1, 2, false, true},
		{"two pages, last", 2, 20, 21, 2, 2, true, false},
		{"page below range clamps up", 0, 20, 100, 1, 5, false, true},
		{"page above range clamps down", 99, 20, 100, 5, 5, true, false},
		{"middle page has both", 3, 10, 100, 3, 10, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nav := newPageNav(tt.page, tt.perPage, tt.total, "/boards/1")
			if nav.CurrentPage != tt.wantPage {
				t.Errorf("CurrentPage = %d, want %d", nav.CurrentPage, tt.wantPage)
			}
			if nav.TotalPages != tt.wantTotal {
				t.Errorf("TotalPages = %d, want %d", nav.TotalPages, tt.wantTotal)
			}
			if nav.HasPrev != tt.wantPrev {
				t.Errorf("HasPrev = %v, want %v", nav.HasPrev, tt.wantPrev)
			}
			if nav.HasNext != tt.wantNext {
				t.Errorf("HasNext = %v, want %v", nav.HasNext, tt.wantNext)
			}
		})
	}
}

// TestTemplatesParse guards against a malformed action in any *.html.tmpl —
// parseTemplates is otherwise only exercised at NewServer() time, so a bad
// {{...}} would ship undetected until first boot.
func TestTemplatesParse(t *testing.T) {
	tpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	for _, page := range []string{"boards_index", "boards_forum", "boards_topic", "boards_new"} {
		if _, ok := tpl[page]; !ok {
			t.Errorf("template %q missing from set", page)
		}
	}
}

// Posting requires a session. The login gate fires before any dependency is
// touched, so we can exercise it with a zero-value handlers and an
// identity-free request.
func TestBoardWritesRequireLogin(t *testing.T) {
	h := &handlers{}
	cases := []struct {
		name    string
		method  string
		target  string
		handler http.HandlerFunc
	}{
		{"new topic GET", http.MethodGet, "/boards/1/new", h.boardNewGet},
		{"new topic POST", http.MethodPost, "/boards/1/new", h.boardNewPost},
		{"reply POST", http.MethodPost, "/boards/1/2/reply", h.boardReplyPost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			w := httptest.NewRecorder()
			tc.handler(w, req)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
			}
			if loc := w.Header().Get("Location"); loc != "/login" {
				t.Errorf("Location = %q, want %q", loc, "/login")
			}
		})
	}
}
