package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nickna/ssh.night.ms/internal/doors/cards"
)

// stripped strips ANSI escapes so we can assert on the plaintext layout —
// the styling itself is verified visually via run.ps1. The goal of these
// tests is to catch column/row regressions in the sprite math.
func stripped(t *testing.T, s string) string {
	t.Helper()
	return ansi.Strip(s)
}

func TestRenderCard_AceOfSpades(t *testing.T) {
	got := stripped(t, RenderCard(cards.Card{Rank: cards.Ace, Suit: cards.Spades}, CardStateNormal))
	want := "┌────┐\n│A   │\n│ ♠  │\n│   A│\n└────┘"
	if got != want {
		t.Fatalf("ace of spades sprite mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRenderCard_TenOfHearts(t *testing.T) {
	got := stripped(t, RenderCard(cards.Card{Rank: cards.Ten, Suit: cards.Hearts}, CardStateNormal))
	want := "┌────┐\n│10  │\n│ ♥  │\n│  10│\n└────┘"
	if got != want {
		t.Fatalf("ten of hearts sprite mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRenderCardBack(t *testing.T) {
	got := stripped(t, RenderCardBack(CardStateNormal))
	want := "┌────┐\n│▒▒▒▒│\n│▒▒▒▒│\n│▒▒▒▒│\n└────┘"
	if got != want {
		t.Fatalf("card back sprite mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRenderCardEmpty(t *testing.T) {
	got := stripped(t, RenderCardEmpty())
	want := "┌────┐\n│    │\n│    │\n│    │\n└────┘"
	if got != want {
		t.Fatalf("empty card sprite mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestJoinCards_Three(t *testing.T) {
	a := RenderCard(cards.Card{Rank: cards.King, Suit: cards.Clubs}, CardStateNormal)
	b := RenderCard(cards.Card{Rank: cards.Two, Suit: cards.Diamonds}, CardStateNormal)
	c := RenderCardBack(CardStateNormal)
	row := stripped(t, JoinCards(a, b, c))
	lines := strings.Split(row, "\n")
	if len(lines) != CardHeight {
		t.Fatalf("expected %d lines, got %d:\n%s", CardHeight, len(lines), row)
	}
	// Each line should be 3 cards (6 cols each) + 2 single-col gaps = 20 cols.
	const wantWidth = CardWidth*3 + 2
	for i, line := range lines {
		// Trim styled width via the ANSI-stripped string; spaces in the gap
		// column must survive trimming, so count runes.
		runes := []rune(line)
		if len(runes) != wantWidth {
			t.Fatalf("line %d width = %d runes, want %d\nline: %q", i, len(runes), wantWidth, line)
		}
	}
	// Spot-check the top border: K|gap|2|gap|back.
	wantTop := "┌────┐ ┌────┐ ┌────┐"
	if lines[0] != wantTop {
		t.Fatalf("top row mismatch\nwant: %q\ngot:  %q", wantTop, lines[0])
	}
}

func TestRenderCard_AllRanksFitInterior(t *testing.T) {
	// Sanity: every rank's sprite must be exactly CardWidth cells wide on
	// every row. Catches a regression if we ever add a rank glyph wider
	// than 2 cells.
	for r := cards.Two; r <= cards.Ace; r++ {
		s := stripped(t, RenderCard(cards.Card{Rank: r, Suit: cards.Clubs}, CardStateNormal))
		for i, line := range strings.Split(s, "\n") {
			if w := len([]rune(line)); w != CardWidth {
				t.Fatalf("rank %d row %d width=%d, want %d (line=%q)", r, i, w, CardWidth, line)
			}
		}
	}
}
