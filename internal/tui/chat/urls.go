package chat

import (
	"net/url"
	"strings"
)

// ExtractImageURLs scans a chat message body for http(s) URLs whose path ends
// in a known image extension and returns the deduplicated list (preserving
// insertion order). Used by the chat screen to schedule inline image fetches.
// Returns nil when nothing matches so callers can short-circuit the empty case.
//
// We deliberately keep the detector conservative — extension-based, scheme
// must be http(s), no parsing of HTML/Markdown — so a misposted link doesn't
// trigger a fetch and so trash bytes ("text://something.png") can't pull a
// URL through the test.
func ExtractImageURLs(body string) []string {
	if body == "" {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, tok := range strings.Fields(body) {
		tok = trimURLPunctuation(tok)
		if !strings.HasPrefix(tok, "http://") && !strings.HasPrefix(tok, "https://") {
			continue
		}
		u, err := url.Parse(tok)
		if err != nil || u.Host == "" {
			continue
		}
		if !hasImageExtension(u.Path) {
			continue
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// trimURLPunctuation strips trailing/leading punctuation we never want to
// include in a URL — common when someone writes "see https://x/y.png." in
// chat. Conservatively keeps everything that could be part of a real URL.
func trimURLPunctuation(s string) string {
	const lead = "([{<\""
	const trail = ".,;:!?)]}>\""
	for len(s) > 0 && strings.ContainsRune(lead, rune(s[0])) {
		s = s[1:]
	}
	for len(s) > 0 && strings.ContainsRune(trail, rune(s[len(s)-1])) {
		s = s[:len(s)-1]
	}
	return s
}

func hasImageExtension(path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return true
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return true
	case strings.HasSuffix(lower, ".gif"):
		return true
	}
	return false
}
