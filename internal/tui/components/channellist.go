package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// ChannelListItem is one row in the sidebar.
type ChannelListItem struct {
	ID      int64
	Name    string
	Display string // pre-formatted label shown in the sidebar (e.g., "@alice" for DM channels). Falls back to "#name" when empty.
	Unread  int    // unread message count since last switch; rendered as a trailing badge when > 0.

	// Presence indicator. Optional — only set for DM channels by the caller
	// (room channels show no dot). Zero value = no indicator rendered.
	ShowPresence bool
	Online       bool
}

// ChannelList is the chat screen's left-rail sidebar. Renders a vertical list
// of joined channels with the active one highlighted. Pure component — input
// handling stays in the parent screen; this just paints.
type ChannelList struct {
	Items    []ChannelListItem
	ActiveID int64
}

const channelListWidth = 20

var (
	channelHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).
				Underline(true)
	channelRowActive = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(theme.ColorYellow)).
				Background(lipgloss.Color(theme.ColorSurfaceAlt))
	channelRowIdle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorAccentDim))
	channelBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(theme.ColorRed))
	channelMarkerActive = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(theme.ColorYellow)).
				Render("▸")
	channelSlotStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorDim))
	channelDotOnline = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorGreen)).
				Render("●")
	channelDotOffline = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.ColorDim)).
				Render("○")
)

// Width returns the fixed column count the sidebar occupies. Centralized so
// the parent screen can subtract it when sizing the chat body.
func (ChannelList) Width() int { return channelListWidth }

// View paints `height` lines of sidebar content. The list is left-aligned and
// padded with empty rows so the column stays the same height as the chat body.
func (c ChannelList) View(height int) string {
	var lines []string
	header := channelHeaderStyle.Render("channels")
	lines = append(lines, padRight(header, channelListWidth))
	lines = append(lines, padRight("", channelListWidth))

	for i, it := range c.Items {
		label := it.Display
		if label == "" {
			label = "#" + it.Name
		}

		// "▸" for current, blank for others. Reserved column so labels line up
		// across rows regardless of marker.
		marker := " "
		if it.ID == c.ActiveID {
			marker = channelMarkerActive
		}
		// Alt+digit slot: 1..9 for the first nine entries, 0 for the tenth.
		// Slots beyond the tenth get a blank cell — Alt+0..9 is the only
		// keyboard accelerator we wire.
		slot := " "
		switch {
		case i < 9:
			slot = string(rune('1' + i))
		case i == 9:
			slot = "0"
		}

		// Reserve space for the unread badge so the badge appears flush-right
		// and the channel label is truncated, not the badge. Active channel
		// never shows a badge — switching to it clears the count.
		var badge string
		if it.ID != c.ActiveID && it.Unread > 0 {
			badge = " " + channelBadgeStyle.Render(formatBadge(it.Unread))
		}
		// Presence dot only for DM channels; rendered before the label.
		var dot string
		if it.ShowPresence {
			if it.Online {
				dot = channelDotOnline + " "
			} else {
				dot = channelDotOffline + " "
			}
		}
		// Account for the fixed prefix: "▸" + "N" + " " = 3 cells, plus dot/badge.
		labelBudget := channelListWidth - 3 - lipgloss.Width(badge) - lipgloss.Width(dot)
		if labelBudget < 1 {
			labelBudget = 1
		}
		label = runewidth.Truncate(label, labelBudget, "…")

		prefix := marker + channelSlotStyle.Render(slot) + " "
		var row string
		if it.ID == c.ActiveID {
			row = prefix + channelRowActive.Render(dot+label)
		} else {
			row = prefix + channelRowIdle.Render(dot+label) + badge
		}
		lines = append(lines, padRight(row, channelListWidth))
	}

	// Pad bottom with empty rows so the column matches body height.
	for len(lines) < height {
		lines = append(lines, padRight("", channelListWidth))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// formatBadge renders an unread count compactly. Capped at "9+" so a runaway
// channel doesn't push the sidebar layout around.
func formatBadge(n int) string {
	switch {
	case n <= 0:
		return ""
	case n >= 10:
		return "9+"
	default:
		return string(rune('0' + n))
	}
}

// padRight extends an SGR-bearing string with trailing spaces so its
// printable width hits `w`. lipgloss.Width counts cells, ignoring ANSI.
func padRight(s string, w int) string {
	dw := lipgloss.Width(s)
	if dw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-dw)
}
