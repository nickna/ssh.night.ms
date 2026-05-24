package screens

import (
	"testing"
	"time"

	"github.com/nickna/ssh.night.ms/internal/data"
)

func TestParseFilters_EmptyInputReturnsNil(t *testing.T) {
	if got := parseFilters(""); got != nil {
		t.Fatalf("empty input should return nil; got %v", got)
	}
	if got := parseFilters("   \t\n  "); got != nil {
		t.Fatalf("whitespace-only input should return nil; got %v", got)
	}
}

func TestParseFilters_BareTokenIsTextFilter(t *testing.T) {
	got := parseFilters("alice")
	want := []data.Filter{{Dim: "text", Text: "alice"}}
	if !filtersEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestParseFilters_AllowedKeysProduceTypedFilters(t *testing.T) {
	cases := []struct {
		in   string
		want data.Filter
	}{
		{"severity:warn", data.Filter{Dim: "severity", Text: "warn"}},
		{"handle:alice", data.Filter{Dim: "handle", Text: "alice"}},
		{"ip:203.0.113.1", data.Filter{Dim: "ip", Text: "203.0.113.1"}},
		{"kind:auth_failure", data.Filter{Dim: "kind", Text: "auth_failure"}},
		{"source:audit", data.Filter{Dim: "source", Text: "audit"}},
		{"text:foo", data.Filter{Dim: "text", Text: "foo"}},
	}
	for _, c := range cases {
		got := parseFilters(c.in)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%q: want [%v], got %v", c.in, c.want, got)
		}
	}
}

func TestParseFilters_KeyIsCaseInsensitive(t *testing.T) {
	got := parseFilters("SEVERITY:warn HANDLE:Alice")
	want := []data.Filter{
		{Dim: "severity", Text: "warn"},
		{Dim: "handle", Text: "Alice"},
	}
	if !filtersEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestParseFilters_UnknownKeyFallsBackToTextWithOriginalToken(t *testing.T) {
	// Preserves the colon in the text so the user sees the whole token
	// matched (rather than just the value-side).
	got := parseFilters("bogus:value")
	want := []data.Filter{{Dim: "text", Text: "bogus:value"}}
	if !filtersEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestParseFilters_EmptyValueDropsFilter(t *testing.T) {
	if got := parseFilters("handle:"); got != nil {
		t.Errorf("empty value should drop the filter; got %v", got)
	}
}

func TestParseFilters_MultipleTokensAreANDed(t *testing.T) {
	got := parseFilters("severity:warn handle:alice ip:203.0.113.1")
	if len(got) != 3 {
		t.Fatalf("want 3 filters; got %d: %v", len(got), got)
	}
}

func TestParseFilters_SinceRelativeDuration(t *testing.T) {
	now := time.Now()
	got := parseFilters("since:1h")
	if len(got) != 1 || got[0].Dim != "since" {
		t.Fatalf("want one since filter; got %v", got)
	}
	delta := now.Sub(got[0].Time)
	if delta < 59*time.Minute || delta > 61*time.Minute {
		t.Errorf("since:1h should yield ~1h ago; delta=%v", delta)
	}
}

func TestParseFilters_SinceExtendedDays(t *testing.T) {
	now := time.Now()
	got := parseFilters("since:2d")
	if len(got) != 1 || got[0].Dim != "since" {
		t.Fatalf("want one since filter; got %v", got)
	}
	delta := now.Sub(got[0].Time)
	want := 2 * 24 * time.Hour
	if delta < want-time.Minute || delta > want+time.Minute {
		t.Errorf("since:2d should yield ~48h ago; delta=%v", delta)
	}
}

func TestParseFilters_SinceExtendedWeeks(t *testing.T) {
	now := time.Now()
	got := parseFilters("since:1w")
	if len(got) != 1 || got[0].Dim != "since" {
		t.Fatalf("want one since filter; got %v", got)
	}
	delta := now.Sub(got[0].Time)
	want := 7 * 24 * time.Hour
	if delta < want-time.Minute || delta > want+time.Minute {
		t.Errorf("since:1w should yield ~7d ago; delta=%v", delta)
	}
}

func TestParseFilters_SinceAbsoluteDate(t *testing.T) {
	got := parseFilters("since:2026-01-15")
	if len(got) != 1 || got[0].Dim != "since" {
		t.Fatalf("want one since filter; got %v", got)
	}
	want := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if !got[0].Time.Equal(want) {
		t.Errorf("since:2026-01-15: want %v, got %v", want, got[0].Time)
	}
}

func TestParseFilters_SinceUnparseableFallsBackToTextWithOriginalToken(t *testing.T) {
	// Lets the user keep typing a partial date without losing characters.
	got := parseFilters("since:2026-")
	if len(got) != 1 || got[0].Dim != "text" || got[0].Text != "since:2026-" {
		t.Errorf("unparseable since should fall back to text:original; got %v", got)
	}
}

func TestParseFilters_UntilSymmetricWithSince(t *testing.T) {
	got := parseFilters("until:1h")
	if len(got) != 1 || got[0].Dim != "until" {
		t.Fatalf("want one until filter; got %v", got)
	}
	if got[0].Time.IsZero() {
		t.Error("until:1h should produce a non-zero time")
	}
}

func TestParseFilters_MixedTextAndTypedTokens(t *testing.T) {
	got := parseFilters("alice severity:warn lockout")
	want := []data.Filter{
		{Dim: "text", Text: "alice"},
		{Dim: "severity", Text: "warn"},
		{Dim: "text", Text: "lockout"},
	}
	if !filtersEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func filtersEqual(a, b []data.Filter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Dim != b[i].Dim || a[i].Text != b[i].Text {
			return false
		}
		// Time-typed filters skip the Time equality check (it's the
		// caller's responsibility to assert that separately) — every
		// test that cares about Time does so explicitly.
	}
	return true
}
