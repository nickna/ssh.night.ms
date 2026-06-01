package components

import (
	"fmt"
	"time"
)

// FormatAge renders a coarse "time since" label: "just now", "5m ago",
// "2h ago", "3d ago". Negative durations (clock skew) clamp to "just now".
// This is the single source for the relative-age labels used across the chat
// log, boards, finance news, and the Web screen's recent-history list.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// FormatRelativeAge labels a timestamp by how long ago it was. Thin wrapper
// over FormatAge for the common case of "how old is this?".
func FormatRelativeAge(t time.Time) string { return FormatAge(time.Since(t)) }
