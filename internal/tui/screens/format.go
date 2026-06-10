package screens

import "github.com/nickna/ssh.night.ms/internal/doors/cards"

// Package-level pure helpers shared across screens. Kept here (rather than
// buried in whichever screen first needed them) so they're discoverable and
// have a single definition.

// plural appends "s" unless n == 1. ASCII regular nouns only.
func plural(noun string, n int) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// truncate shortens s to at most n bytes, ending in an ellipsis when cut. Byte-
// based, so it assumes the mostly-ASCII content (symbols, titles) it's used on.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

// truncateRunes is the rune-aware variant of truncate, for content that can
// carry non-ASCII (NWS alert area names, user-supplied text). Counts runes so
// a multi-byte character near the cut point can't be split mid-sequence.
func truncateRunes(s string, max int) string {
	if max <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// handToStrings renders a card hand to its display strings, one per card.
// Shared by the casino screens (blackjack, video poker).
func handToStrings(h []cards.Card) []string {
	out := make([]string, len(h))
	for i, c := range h {
		out[i] = c.String()
	}
	return out
}

// max0 clamps a negative int up to 0.
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// clamp constrains v to the inclusive range [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampIndex returns v if it's a valid index into a slice of length max,
// otherwise 0. Use when an out-of-range value should fall back to the FIRST
// element — e.g. a persisted enum/option index, or a list cursor that should
// jump to the top when its old position no longer exists.
func clampIndex(v, max int) int {
	if v < 0 || v >= max {
		return 0
	}
	return v
}

// clampCursor keeps a list cursor in range after the list's length changed,
// clamping into [0, n-1] (and to 0 when the list is empty). Use when an out-of-
// range cursor should stay at the LAST element rather than jump to the top —
// the right feel after a list shrinks under the cursor.
func clampCursor(v, n int) int {
	if v >= n {
		v = n - 1
	}
	if v < 0 {
		return 0
	}
	return v
}
