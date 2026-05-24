// Package components — modal.go provides the centered-overlay helper used
// by Boards' compose form. Bubble Tea / lipgloss have no native concept of
// "render foreground on top of background", so we composite two
// already-rendered strings ourselves using ansi-aware cell math.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// padRight is shared with channellist.go in this package.

// DimSGR returns s with every SGR sequence stripped and the plain text
// re-styled in dimHex. The effect: all base content reads as uniformly
// muted, regardless of what foreground/background/bold styling lipgloss
// originally baked in. The user explicitly chose this "true alpha-blend"
// behaviour over leaving accented runs bright — see the Boards refactor
// plan.
//
// dimHex must be a hex color literal of the form "#RRGGBB". Empty input
// returns empty; never panics on malformed colors (lipgloss tolerates
// unknown color strings by falling back to default).
func DimSGR(s, dimHex string) string {
	if s == "" {
		return ""
	}
	stripped := ansi.Strip(s)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(dimHex))
	// Render each line separately so the open/close SGR pair brackets the
	// line cleanly — multi-line Render on lipgloss handles this internally
	// but doing it ourselves also defends against padding/border insertion.
	lines := strings.Split(stripped, "\n")
	for i, ln := range lines {
		lines[i] = dim.Render(ln)
	}
	return strings.Join(lines, "\n")
}

// Overlay returns base with modal composited on top, centered in a w×h
// viewport. Both inputs are arbitrary multi-line strings; we split each on
// \n, find the modal's effective cell dimensions via ansi.StringWidth, and
// splice modal lines into the matching base lines using ansi.Cut so styled
// cells in base outside the overlay region survive intact.
//
// base is expected to already be styled (and dimmed via DimSGR if the
// caller wants the underlying scene muted). The caller is responsible for
// making sure modal carries its own background — otherwise the cells will
// "show through" any SGR runs that happen to share their column range.
//
// If the modal is wider or taller than w/h it is clamped: extra columns /
// rows are dropped from the right / bottom edges. That keeps the function
// total — no error return — for the screen renderer that calls it every
// frame.
func Overlay(base, modal string, w, h int) string {
	if modal == "" || w <= 0 || h <= 0 {
		return base
	}
	baseLines := splitToHeight(base, h)
	modalLines := strings.Split(modal, "\n")

	// Modal dimensions in display cells.
	modalW := 0
	for _, ml := range modalLines {
		if cw := ansi.StringWidth(ml); cw > modalW {
			modalW = cw
		}
	}
	if modalW > w {
		modalW = w
	}
	modalH := len(modalLines)
	if modalH > h {
		modalH = h
		modalLines = modalLines[:modalH]
	}

	left := (w - modalW) / 2
	if left < 0 {
		left = 0
	}
	top := (h - modalH) / 2
	if top < 0 {
		top = 0
	}

	for i := 0; i < modalH; i++ {
		row := top + i
		baseRow := baseLines[row]
		// Left slice of the base row up to the modal's left edge.
		leftPart := ansi.Cut(baseRow, 0, left)
		leftPart = padRight(leftPart, left)
		// Right slice from the modal's right edge to the viewport edge.
		// Pad so the row is exactly `w` cells wide regardless of how
		// short the underlying base row was (otherwise rows where base
		// ran out of cells before reaching the modal collapse short).
		rightStart := left + modalW
		rightPart := ansi.Cut(baseRow, rightStart, w)
		rightPart = padRight(rightPart, w-rightStart)

		modalLine := ansi.Truncate(modalLines[i], modalW, "")
		// Pad the modal line so each row occupies exactly modalW cells, so
		// rightPart starts at the expected column even when a particular
		// modal line is shorter (e.g. the empty inner-padding row).
		modalLine = padRight(modalLine, modalW)

		baseLines[row] = leftPart + modalLine + rightPart
	}
	return strings.Join(baseLines, "\n")
}

// splitToHeight splits s on \n and pads/truncates to exactly h rows so the
// caller doesn't have to special-case "base is shorter than viewport".
func splitToHeight(s string, h int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) >= h {
		return lines[:h]
	}
	out := make([]string, h)
	copy(out, lines)
	// remaining entries are empty strings (zero value of string slice) —
	// no further work needed
	return out
}
