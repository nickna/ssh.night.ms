package screens

import (
	"strconv"
	"strings"
	"time"

	"github.com/nickna/ssh.night.ms/internal/data"
)

// parseFilters tokenises the Events tab's filter input and returns the
// data.Filter slice the query layer consumes. Tokens are whitespace-split;
// `key:value` tokens with an allowlisted key map to typed filters; any
// other token (including unknown keys) falls back to a free-text filter
// over the row's details/kind/actor/subject columns.
//
// Examples:
//
//	"severity:warn"          → [{Dim:"severity", Text:"warn"}]
//	"handle:alice ip:1.2.3.4" → two filters AND'd
//	"since:1h"               → time-typed filter at time.Now().Add(-1h)
//	"foo"                    → [{Dim:"text", Text:"foo"}]
//	"unknownkey:x"           → [{Dim:"text", Text:"unknownkey:x"}]
//	""                       → nil
func parseFilters(input string) []data.Filter {
	var out []data.Filter
	for _, tok := range strings.Fields(input) {
		if f, ok := parseToken(tok); ok {
			out = append(out, f)
		}
	}
	return out
}

// parseToken classifies one whitespace-delimited token. Returns ok=false
// only for entirely empty results (the parseFilters loop drops those).
func parseToken(tok string) (data.Filter, bool) {
	key, val, hasColon := strings.Cut(tok, ":")
	if !hasColon {
		return data.Filter{Dim: "text", Text: tok}, true
	}
	key = strings.ToLower(strings.TrimSpace(key))
	val = strings.TrimSpace(val)
	if val == "" {
		return data.Filter{}, false
	}
	switch key {
	case "severity", "handle", "ip", "kind", "source", "text":
		return data.Filter{Dim: key, Text: val}, true
	case "since", "until":
		t, ok := parseFilterTime(val)
		if !ok {
			// Unparseable time → fall back to text search of the whole
			// original token so the user doesn't lose typed characters
			// while typing a partial timestamp.
			return data.Filter{Dim: "text", Text: tok}, true
		}
		return data.Filter{Dim: key, Time: t}, true
	default:
		// Unknown key → treat the whole token as text. Keeps the parser
		// permissive: users can paste a UUID, an email, anything, and
		// it'll match somewhere in the details payload via the text dim.
		return data.Filter{Dim: "text", Text: tok}, true
	}
}

// parseFilterTime resolves a time value for since/until. Accepts:
//   - Go duration syntax (`1h`, `30m`, `5s`) → time.Now().Add(-d)
//   - Extended duration with `d` (days) or `w` (weeks) suffix
//   - ISO date `YYYY-MM-DD` → midnight UTC of that day
//   - RFC3339 timestamp → exact UTC instant
//
// Returns (zero, false) when none of the above match.
func parseFilterTime(val string) (time.Time, bool) {
	if d, err := parseExtendedDuration(val); err == nil {
		return time.Now().Add(-d), true
	}
	if t, err := time.Parse("2006-01-02", val); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, val); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// parseExtendedDuration adds `d` (days) and `w` (weeks) to time.ParseDuration's
// vocabulary. Operators think in days more than they think in hours when
// triaging an incident over the last day or two.
func parseExtendedDuration(val string) (time.Duration, error) {
	if n := len(val); n >= 2 {
		switch val[n-1] {
		case 'd':
			f, err := strconv.ParseFloat(val[:n-1], 64)
			if err == nil {
				return time.Duration(f * 24 * float64(time.Hour)), nil
			}
		case 'w':
			f, err := strconv.ParseFloat(val[:n-1], 64)
			if err == nil {
				return time.Duration(f * 7 * 24 * float64(time.Hour)), nil
			}
		}
	}
	return time.ParseDuration(val)
}
