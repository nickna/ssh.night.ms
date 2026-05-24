// Package components — cabinet.go renders the standard cabinet frame every
// card game lives inside. A cabinet has:
//
//   - a centered title bar tinted with the game's felt color
//   - a body region (the screen's existing render, padded inward)
//   - a wallet/bet line with optional "+payout" chip when a win has just settled
//   - a hint footer (keybinds, italic + muted)
//
// All three card-game screens (Blackjack, Video Poker, Hold'em) call
// CabinetFrame at the end of their View(). Sharing the chrome means the
// felt color is the only per-game variation; nothing else can drift.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// CabinetWallet carries the wallet row contents. Payout is rendered as a
// gold "+N" chip when > 0; pass 0 (and an empty PayoutLabel) outside of
// the coin-shower window.
type CabinetWallet struct {
	Bet         int32
	Total       int64
	Payout      int32  // >0 to show the +chip; 0 to hide
	PayoutLabel string // optional context, e.g. "BLACKJACK +15"; ignored when Payout==0
}

// CabinetOpts configures one frame. Width defaults to 60 if 0; FeltAccent
// defaults to ColorAccent.
type CabinetOpts struct {
	Title      string
	Width      int
	FeltAccent string // hex color for the title bar tint
	Wallet     CabinetWallet
	Footer     string // keybind hint line
}

// CabinetFrame wraps body in the cabinet chrome and returns the composed
// multi-line string. Body is rendered as-is (already padded by the caller
// where it matters); we add a left+right gutter of one space.
func CabinetFrame(body string, opts CabinetOpts) string {
	width := opts.Width
	if width <= 0 {
		width = 60
	}
	felt := opts.FeltAccent
	if felt == "" {
		felt = theme.ColorAccent
	}

	titleBar := renderCabinetTitle(opts.Title, width, felt)
	walletRow := renderCabinetWallet(opts.Wallet, width)
	hint := renderCabinetHint(opts.Footer, width)

	bodyBlock := indentBody(body, width)

	return strings.Join([]string{titleBar, bodyBlock, walletRow, hint}, "\n")
}

func renderCabinetTitle(title string, width int, felt string) string {
	bar := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorText)).
		Background(lipgloss.Color(felt)).
		Bold(true).
		Width(width).
		Align(lipgloss.Center)
	return bar.Render(strings.ToUpper(title))
}

func renderCabinetWallet(w CabinetWallet, width int) string {
	betStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Bold(true)
	totalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorGreen)).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	chipStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorBackground)).
		Background(lipgloss.Color(theme.ColorCardHeld)).
		Bold(true).
		Padding(0, 1)

	left := fmt.Sprintf("%s %s   %s %s",
		labelStyle.Render("BET"), betStyle.Render(fmt.Sprintf("%d", w.Bet)),
		labelStyle.Render("TOTAL"), totalStyle.Render(fmt.Sprintf("%d", w.Total)))

	right := ""
	if w.Payout > 0 {
		chipText := fmt.Sprintf("+%d", w.Payout)
		if w.PayoutLabel != "" {
			chipText = fmt.Sprintf("%s +%d", w.PayoutLabel, w.Payout)
		}
		right = chipStyle.Render(chipText)
	}

	return spaceBetween(left, right, width)
}

func renderCabinetHint(text string, width int) string {
	if text == "" {
		return ""
	}
	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.ColorMuted)).
		Italic(true).
		Width(width).
		Align(lipgloss.Center)
	return hint.Render(text)
}

// spaceBetween places left at the start of a width-wide row and right
// flush against the right edge, padding the middle with spaces. If the
// combined cell width exceeds the available width, right is dropped.
func spaceBetween(left, right string, width int) string {
	lw := ansi.StringWidth(left)
	rw := ansi.StringWidth(right)
	if lw+rw+1 > width {
		// Fall back to left-only — better than truncating the wallet read.
		gap := width - lw
		if gap < 0 {
			gap = 0
		}
		return left + strings.Repeat(" ", gap)
	}
	gap := width - lw - rw
	return left + strings.Repeat(" ", gap) + right
}

// indentBody pads each body line so the cabinet has a one-space gutter on
// each side and trims to the inner width. Body lines wider than width-2
// pass through (no truncation) so we don't accidentally clip a card row.
func indentBody(body string, width int) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = " " + l
	}
	return strings.Join(out, "\n")
}
