package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWeatherTemplateParses guards the weather template alongside the boards
// set — a malformed {{...}} would otherwise only surface at NewServer() time.
func TestWeatherTemplateParses(t *testing.T) {
	tpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	if _, ok := tpl["weather"]; !ok {
		t.Errorf("template %q missing from set", "weather")
	}
}

// The weather page requires a session. The login gate fires before any
// dependency is touched, so a zero-value handlers + identity-free request
// exercises it without a DB.
func TestWeatherRequiresLogin(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	w := httptest.NewRecorder()
	h.weatherIndex(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want %q", loc, "/login")
	}
}

func TestSeverityClass(t *testing.T) {
	cases := map[string]string{
		"Extreme":  "sev-extreme",
		"extreme":  "sev-extreme",
		"Severe":   "sev-severe",
		"Moderate": "sev-moderate",
		"Minor":    "sev-minor",
		"Unknown":  "sev-minor",
		"":         "sev-minor",
	}
	for in, want := range cases {
		if got := severityClass(in); got != want {
			t.Errorf("severityClass(%q) = %q, want %q", in, got, want)
		}
	}
}
