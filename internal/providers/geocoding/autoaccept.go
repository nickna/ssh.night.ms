package geocoding

import "strings"

// AutoAccept decides whether a geocoder result list is unambiguous enough
// that the UI can commit the top hit without a confirmation picker.
//
// Returns the chosen result and true when both:
//
//  1. The top result's Name case-insensitively equals the trimmed query,
//     so the user typed the exact place name the geocoder anchored on.
//  2. No other result in the list shares that Name (case-insensitive),
//     so there are no "Springfield, MO" vs "Springfield, IL" collisions
//     hiding in the tail.
//
// "Paris" → multi-match same name → picker.
// "San Francisco" → top match exact, no siblings → auto-accept.
// "san francisco" → casing-insensitive match → auto-accept.
// "SF" → top match Name="San Francisco", query="SF" → no match → picker.
//
// An empty results slice returns (nil, false).
func AutoAccept(query string, results []Result) (*Result, bool) {
	if len(results) == 0 {
		return nil, false
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, false
	}
	top := results[0]
	if !strings.EqualFold(top.Name, q) {
		return nil, false
	}
	for _, r := range results[1:] {
		if strings.EqualFold(r.Name, top.Name) {
			return nil, false
		}
	}
	return &top, true
}
