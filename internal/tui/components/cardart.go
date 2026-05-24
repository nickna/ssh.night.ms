// Package components — cardart.go renders a single playing card as a 5-line,
// 6-column sprite with state-aware borders and suit-colored faces. Every
// card-based door game (Blackjack, Video Poker, Hold'em) consumes this so
// the visual treatment stays consistent and a fourth game later is a
// screen-only edit.
//
// Sprite layout (6 cols wide × 5 rows tall):
//
//	┌────┐
//	│A   │   rank top-left
//	│ ♠  │   suit pip centered
//	│   A│   rank bottom-right
//	└────┘
//
// Rank "10" is the only two-char rank — it occupies both interior corner
// columns of its row. All other ranks render as a single glyph.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// CardState selects the border + ink treatment for a card.
type CardState int

const (
	CardStateNormal  CardState = iota // accent border, suit-colored ink
	CardStateHeld                     // gold border — Video Poker holds
	CardStateWinning                  // gold border + bold — Hold'em best-5, VP winning contributors
	CardStateDimmed                   // muted border + ink — losing hand on settle
)

// CardWidth/CardHeight are the rendered sprite dimensions in cells. Screens
// can use these for layout math (e.g. padding to center a hand).
const (
	CardWidth  = 6
	CardHeight = 5
)

// borderColor picks the lipgloss color for a card's border based on state.
func borderColor(st CardState) lipgloss.Color {
	switch st {
	case CardStateHeld, CardStateWinning:
		return lipgloss.Color(theme.ColorCardHeld)
	case CardStateDimmed:
		return lipgloss.Color(theme.ColorDim)
	default:
		return lipgloss.Color(theme.ColorAccent)
	}
}

// suitColor picks the ink color for the rank+pip glyphs. Hearts/diamonds
// read as red; spades/clubs as the standard text color. Dimmed cards drop
// to the muted palette regardless of suit so they recede on a loss.
func suitColor(suit cards.Suit, st CardState) lipgloss.Color {
	if st == CardStateDimmed {
		return lipgloss.Color(theme.ColorDim)
	}
	switch suit {
	case cards.Hearts, cards.Diamonds:
		return lipgloss.Color(theme.ColorSuitRed)
	default:
		return lipgloss.Color(theme.ColorSuitInk)
	}
}

// rankGlyph returns the display string for a rank. Length is 1 except for
// Ten which is "10" (two cells wide).
func rankGlyph(r cards.Rank) string {
	switch r {
	case cards.Ace:
		return "A"
	case cards.King:
		return "K"
	case cards.Queen:
		return "Q"
	case cards.Jack:
		return "J"
	case cards.Ten:
		return "10"
	default:
		return string(rune('0' + int(r)))
	}
}

// suitGlyph returns the Unicode pip for a suit.
func suitGlyph(s cards.Suit) string {
	switch s {
	case cards.Clubs:
		return "♣"
	case cards.Diamonds:
		return "♦"
	case cards.Hearts:
		return "♥"
	case cards.Spades:
		return "♠"
	}
	return "?"
}

// RenderCard returns the 5-line sprite for one card in the given state.
// The returned string contains 4 newlines (no trailing newline). Callers
// concatenate cards horizontally with JoinCards.
func RenderCard(c cards.Card, st CardState) string {
	border := lipgloss.NewStyle().Foreground(borderColor(st))
	if st == CardStateWinning {
		border = border.Bold(true)
	}
	ink := lipgloss.NewStyle().Foreground(suitColor(c.Suit, st)).Bold(true)

	rank := rankGlyph(c.Rank)
	pip := suitGlyph(c.Suit)

	// Interior is 4 cells wide. For one-char rank the rank sits in col 0
	// (top row) and col 3 (bottom row); for "10" it occupies cols 0-1 and
	// cols 2-3 respectively. The middle row centers the pip at col 1.
	topInner := padRight(rank, 4)
	botInner := padLeft(rank, 4)
	midInner := " " + pip + "  "

	top := border.Render("┌────┐")
	bot := border.Render("└────┘")
	pipe := border.Render("│")

	rowTop := pipe + ink.Render(topInner) + pipe
	rowMid := pipe + ink.Render(midInner) + pipe
	rowBot := pipe + ink.Render(botInner) + pipe

	return strings.Join([]string{top, rowTop, rowMid, rowBot, bot}, "\n")
}

// RenderCardBack returns the 5-line sprite for a face-down card. The body
// is filled with a hatched pattern so it never visually flips into another
// face-up card mid-render.
func RenderCardBack(st CardState) string {
	border := lipgloss.NewStyle().Foreground(borderColor(st))
	hatch := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))

	top := border.Render("┌────┐")
	bot := border.Render("└────┘")
	pipe := border.Render("│")
	body := pipe + hatch.Render("▒▒▒▒") + pipe

	return strings.Join([]string{top, body, body, body, bot}, "\n")
}

// RenderCardEmpty returns the 5-line sprite for an empty slot (e.g. a
// pre-flop community card position in Hold'em). Always dim, always blank.
func RenderCardEmpty() string {
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	top := border.Render("┌────┐")
	bot := border.Render("└────┘")
	pipe := border.Render("│")
	body := pipe + "    " + pipe
	return strings.Join([]string{top, body, body, body, bot}, "\n")
}

// JoinCards lays out N pre-rendered card sprites side-by-side with a single
// blank column between each. Each input must be exactly CardHeight lines
// (RenderCard / RenderCardBack / RenderCardEmpty all satisfy this).
func JoinCards(sprites ...string) string {
	if len(sprites) == 0 {
		return ""
	}
	pieces := make([]string, 0, len(sprites)*2-1)
	gap := strings.Repeat(" \n", CardHeight-1) + " " // 1-col-wide spacer, CardHeight tall
	for i, s := range sprites {
		if i > 0 {
			pieces = append(pieces, gap)
		}
		pieces = append(pieces, s)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, pieces...)
}

// padLeft right-aligns s in a field of cell-width w. Uses lipgloss.Width
// so wide-unicode glyphs count correctly. (padRight already lives in
// channellist.go in this same package.)
func padLeft(s string, w int) string {
	dw := lipgloss.Width(s)
	if dw >= w {
		return s
	}
	return strings.Repeat(" ", w-dw) + s
}
