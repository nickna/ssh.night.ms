// Package browser holds the pure (no-Bubble-Tea) building blocks the Browser
// screen relies on: URL classification, back/forward history, and an LRU
// cache of rendered articles. Lives outside the screens package so each
// piece can be unit-tested without spinning a tea.Program.
package browser

import (
	"regexp"
	"strings"
)

// hostLike matches "example.com", "sub.example.co.uk", with an optional
// path/query/fragment. Anchored — the whole input must look like a host
// for IsLikelyURL to return true. IPv4 addresses are recognized separately.
var hostLike = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,}(/.*)?$`)

// ipv4Like matches a bare IPv4 address with an optional port and path.
var ipv4Like = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}(:\d+)?(/.*)?$`)

// IsLikelyURL is the URL-bar's URL-vs-search-query discriminator. A leading
// "?" is the explicit "force search" prefix, so "?go.dev" searches for
// literal "go.dev". Anything starting with http:// or https:// is a URL.
// Otherwise the host-like regex decides.
func IsLikelyURL(input string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return false
	}
	if strings.HasPrefix(input, "?") {
		return false
	}
	low := strings.ToLower(input)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return true
	}
	if strings.ContainsAny(input, " \t") {
		return false
	}
	return hostLike.MatchString(input) || ipv4Like.MatchString(input)
}

// Normalize prepends "https://" when no scheme is present. Returns the input
// unchanged when it already has a scheme. Trims surrounding whitespace.
func Normalize(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	low := strings.ToLower(input)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return input
	}
	return "https://" + input
}

// StripQueryPrefix removes a leading "?" used as the force-search hint, so
// the search engine receives the literal query.
func StripQueryPrefix(input string) string {
	return strings.TrimPrefix(strings.TrimSpace(input), "?")
}
