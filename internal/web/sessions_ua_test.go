package web

import "testing"

func TestShortUA(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want string
	}{
		{"empty", "", "—"},
		{"chrome on macOS",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
			"Chrome on macOS"},
		{"firefox on windows",
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
			"Firefox on Windows"},
		{"edge on windows (must not match chrome first)",
			"Mozilla/5.0 (Windows NT 10.0; Win64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36 Edg/122.0.0.0",
			"Edge on Windows"},
		{"safari on iOS",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			"Safari on iOS"},
		{"curl no os",
			"curl/8.4.0",
			"curl"},
		{"unknown",
			"BBSBot/1.0",
			"Unknown browser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortUA(tc.ua); got != tc.want {
				t.Errorf("shortUA(%q) = %q, want %q", tc.ua, got, tc.want)
			}
		})
	}
}
