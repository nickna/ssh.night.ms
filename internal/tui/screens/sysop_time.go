package screens

import "time"

// Sysop console timestamps are always rendered in UTC, NOT the viewing
// sysop's display-zone preference. This is deliberate: the moderation /
// forensic surface is correlated against the slog-JSON audit sink and raw
// Postgres rows, both of which are UTC. A timestamp that silently shifted
// with whichever sysop happened to be logged in would make cross-referencing
// incident timelines error-prone. These helpers centralize the layouts so
// every tab (events, users, bans, metrics) renders the same zone the same way.
//
// Three layouts, by available width:
//   - sysopTS:      "2006-01-02 15:04:05 UTC" — full, labeled. Detail views.
//   - sysopTSMin:   "2006-01-02 15:04 UTC"    — minute precision, labeled.
//   - sysopTSClock: "15:04:05"                — time-only, for rows already
//     under a labeled-UTC heading (no per-row label needed).
func sysopTS(t time.Time) string      { return t.UTC().Format("2006-01-02 15:04:05 UTC") }
func sysopTSMin(t time.Time) string   { return t.UTC().Format("2006-01-02 15:04 UTC") }
func sysopTSClock(t time.Time) string { return t.UTC().Format("15:04:05") }
