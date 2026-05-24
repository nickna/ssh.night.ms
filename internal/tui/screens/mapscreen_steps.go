package screens

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/providers/routing"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// stepsPanelBorder is the bordered container that frames the right-hand
// side panel when showSteps is active. Width is exact so the JoinHorizontal
// in renderBrowseView puts the map flush against the panel's left edge.
var stepsPanelBorder = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color(theme.ColorAccentDim)).
	Width(stepsPanelWidth).
	Padding(0, 1)

// visibleStepRows is how many step lines fit in the side panel. The header
// (distance / ETA / mode + blank) eats 4 rows; the rest is steps. Mosaic
// row count == sess.Height - 6 so it's known here without a cell render.
func (m *MapScreen) visibleStepRows() int {
	rows := m.sess.Height - 6
	if rows < 6 {
		rows = 6
	}
	// Panel content rows = rows; border adds 2 → already accounted for in
	// the lipgloss height calc. Subtract header (3 lines) + 1 blank.
	avail := rows - 4
	if avail < 1 {
		avail = 1
	}
	return avail
}

// renderStepsPanel builds the side-panel block. Empty route returns "" so
// renderBrowseView can no-op cleanly when the panel shouldn't be drawn.
func (m *MapScreen) renderStepsPanel() string {
	if m.route == nil {
		return ""
	}
	numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	headStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))

	innerW := stepsPanelWidth - 2 // account for padding

	var lines []string
	lines = append(lines,
		headStyle.Render(formatRouteSummary(m.route)),
		dimStyle.Render(fmt.Sprintf("%d steps", len(m.route.Steps))),
		"",
	)

	steps := m.route.Steps
	if len(steps) == 0 {
		lines = append(lines, dimStyle.Render("(no step list — ORS returned only the polyline)"))
	} else {
		visible := m.visibleStepRows()
		end := m.stepsScroll + visible
		if end > len(steps) {
			end = len(steps)
		}
		for i := m.stepsScroll; i < end; i++ {
			lines = append(lines, formatStepLine(i, steps[i], innerW, numStyle, dimStyle))
		}
		// Scroll affordance — show "↑ N more" / "↓ N more" hints when there's
		// content beyond the visible window so the user knows to scroll.
		if m.stepsScroll > 0 {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  ↑ %d above (k/PgUp)", m.stepsScroll)))
		}
		if end < len(steps) {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  ↓ %d below (j/PgDn)", len(steps)-end)))
		}
	}

	return stepsPanelBorder.Render(strings.Join(lines, "\n"))
}

// formatStepLine renders a single step into two lines that fit innerW:
//
//	" 1. Head north on Main St"
//	"      120 m · 15 s"
//
// runewidth-truncated so longer instructions don't wrap and break the join.
func formatStepLine(idx int, s routing.Step, innerW int, numStyle, dimStyle lipgloss.Style) string {
	prefix := fmt.Sprintf("%2d. ", idx+1)
	body := s.Instruction
	if s.Name != "" && !strings.Contains(strings.ToLower(s.Instruction), strings.ToLower(s.Name)) {
		body = s.Instruction + " · " + s.Name
	}
	bodyMax := innerW - lipgloss.Width(prefix)
	if bodyMax < 8 {
		bodyMax = 8
	}
	body = runewidth.Truncate(body, bodyMax, "…")
	detail := fmt.Sprintf("    %s · %s", formatDistanceShort(s.DistanceMeters), formatDurationShort(s.DurationSeconds))
	return numStyle.Render(prefix) + body + "\n" + dimStyle.Render(detail)
}

// formatDistanceShort returns "120 m" / "1.2 km" — compact for the side
// panel where width is tight.
func formatDistanceShort(meters float64) string {
	if meters >= 1000 {
		return fmt.Sprintf("%.1f km", meters/1000)
	}
	return fmt.Sprintf("%.0f m", meters)
}

// formatDurationShort returns "15s" / "3m" / "1h05m".
func formatDurationShort(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	mins := int(seconds / 60)
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
}
