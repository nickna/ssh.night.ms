package screens

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// viewFinger renders the read-only profile viewer used when the user opens
// /finger @handle from chat. Layout: header on top, fields below in two
// columns (label + value).
func (m *Profile) viewFinger() string {
	w := m.sess.Width
	h := m.availableHeight()
	if m.snap == nil {
		body := lipgloss.NewStyle().
			Italic(true).
			Foreground(lipgloss.Color(theme.ColorDim)).
			Render("no such user — Esc / Enter to return to the lobby")
		title := lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(theme.ColorAccent)).
			Render("── finger " + m.viewingHandle + " ──")
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
			lipgloss.JoinVertical(lipgloss.Center, title, "", body))
	}

	header := "── finger @" + m.snap.Handle
	if m.snap.IsSysop {
		header += " (sysop)"
	}
	header += " ──"
	headerStyled := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(theme.ColorAccent)).Render(header)

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))

	rows := []string{headerStyled, ""}
	addRow := func(label, value string) {
		if value == "" {
			return
		}
		rows = append(rows, labelStyle.Render(fmt.Sprintf("  %-12s", label))+valueStyle.Render(value))
	}
	// Joined + last-seen render in the VIEWER's zone + format (read from
	// the session-cached prefs). The subject's time-zone line below uses
	// the subject's zone but the viewer's clock format.
	viewerPrefs := m.sess.DisplayPrefs
	addRow("joined", viewerPrefs.FormatDate(m.snap.CreatedAt))
	if m.snap.LastSeenAt.IsZero() {
		addRow("last seen", "<never>")
	} else {
		addRow("last seen", viewerPrefs.FormatDateTime(m.snap.LastSeenAt))
	}
	addRow("real name", m.snap.RealName)
	addRow("location", m.snap.Location)
	if m.snap.Bio != "" {
		wrapped := wrapBio(m.snap.Bio, 56)
		// Wrap multi-line bio under one label.
		first := wrapped[0]
		addRow("bio", first)
		for _, ln := range wrapped[1:] {
			rows = append(rows, strings.Repeat(" ", 16)+valueStyle.Render(ln))
		}
	}
	if m.snap.TimeZoneID != "" {
		// LoadLocation up front as an "is this id even valid" check —
		// otherwise FormatClock would silently fall back to UTC and the
		// rendered time would lie about the subject's zone.
		if _, err := time.LoadLocation(m.snap.TimeZoneID); err == nil {
			subjectClock := session.DisplayPrefs{
				TimeZoneID:  m.snap.TimeZoneID,
				ClockFormat: viewerPrefs.ClockFormat,
			}
			addRow("local time", fmt.Sprintf("%s (%s)",
				subjectClock.FormatClock(time.Now()), m.snap.TimeZoneID))
		}
	}
	addRow("stats", fmt.Sprintf("%d chat / %d topics / %d posts",
		m.snap.ChatMessageCount, m.snap.TopicCount, m.snap.PostCount))

	rows = append(rows, "", lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorDim)).
		Render("Esc / Enter to return to the lobby"))

	body := strings.Join(rows, "\n")
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, body)
}

// wrapBio breaks bio text into width-bounded lines preserving word
// boundaries. Returns at least one element so the caller can index [0].
func wrapBio(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		line := ""
		lineW := 0
		for _, word := range strings.Fields(paragraph) {
			ww := len([]rune(word))
			if line == "" {
				line, lineW = word, ww
				continue
			}
			if lineW+1+ww <= width {
				line += " " + word
				lineW += 1 + ww
				continue
			}
			out = append(out, line)
			line, lineW = word, ww
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
