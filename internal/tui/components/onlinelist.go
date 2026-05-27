package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// OnlineList renders the right-rail "online (N)" sidebar on the chat screen.
// Backed by the global PresenceService.OnlineHandles list; self is pinned to
// the top with a "(you)" marker.
type OnlineList struct {
	// Handles is the lowercased set of currently-online users, including
	// self. Order is normalized inside View().
	Handles []string
	Self    string
}

const onlineListWidth = 16

var (
	onlineHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).
				Underline(true)
	onlineSelfStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(theme.ColorYellow))
	onlineRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorText))
	onlineMarkerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorDim))
)

// Width returns the column count the online sidebar occupies.
func (OnlineList) Width() int { return onlineListWidth }

// View paints `height` lines. Self is pinned to the top, then everyone else
// alphabetical, then blank rows to fill the column.
func (o OnlineList) View(height int) string {
	header := onlineHeaderStyle.Render("online (" + intStr(len(o.Handles)) + ")")
	var lines []string
	lines = append(lines, padRight(header, onlineListWidth))
	lines = append(lines, padRight("", onlineListWidth))

	ordered := orderOnline(o.Handles, o.Self)
	for _, h := range ordered {
		marker := onlineMarkerStyle.Render("·")
		var row string
		if strings.EqualFold(h, o.Self) {
			row = marker + " " + onlineSelfStyle.Render(runewidth.Truncate(h, onlineListWidth-3, "…"))
		} else {
			row = marker + " " + onlineRowStyle.Render(runewidth.Truncate(h, onlineListWidth-3, "…"))
		}
		lines = append(lines, padRight(row, onlineListWidth))
	}

	for len(lines) < height {
		lines = append(lines, padRight("", onlineListWidth))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// orderOnline pins self to the top, then sorts the rest alphabetically. Pure
// so the chat screen can keep the slice stable across re-renders.
func orderOnline(handles []string, self string) []string {
	out := make([]string, 0, len(handles))
	seen := make(map[string]bool, len(handles))
	selfLower := strings.ToLower(self)
	for _, h := range handles {
		k := strings.ToLower(h)
		if seen[k] {
			continue
		}
		seen[k] = true
		if k == selfLower {
			// Pin self to the top.
			out = append([]string{h}, out...)
			continue
		}
		// Insert sorted; the list is tiny so a linear scan is fine.
		inserted := false
		for i, ex := range out {
			if i == 0 && strings.EqualFold(ex, self) {
				continue
			}
			if strings.ToLower(h) < strings.ToLower(ex) {
				out = append(out[:i], append([]string{h}, out[i:]...)...)
				inserted = true
				break
			}
		}
		if !inserted {
			out = append(out, h)
		}
	}
	return out
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
