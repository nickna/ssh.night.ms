package components

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// truecolorRenderer is a lipgloss renderer that always emits 24-bit SGR.
// Needed in tests because the default renderer detects no TTY and falls
// back to NoColor (which would strip every Foreground() we render).
func truecolorRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	return r
}

func TestDimSGR(t *testing.T) {
	t.Parallel()
	t.Run("empty returns empty", func(t *testing.T) {
		t.Parallel()
		if got := DimSGR("", "#777777"); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("strips styling and rewraps plain runes", func(t *testing.T) {
		t.Parallel()
		r := truecolorRenderer()
		styled := r.NewStyle().Foreground(lipgloss.Color("#FF7DB0")).Bold(true).Render("hello")
		got := DimSGR(styled, "#777777")
		if ansi.Strip(got) != "hello" {
			t.Fatalf("expected stripped text 'hello', got %q (raw %q)", ansi.Strip(got), got)
		}
		// Should not still carry the original pink/bold codes — they
		// would survive only if we forgot to strip first.
		if strings.Contains(got, "255;125;176") || strings.Contains(got, ";1m") {
			t.Fatalf("expected original SGR stripped, got %q", got)
		}
	})

	t.Run("preserves line breaks", func(t *testing.T) {
		t.Parallel()
		got := DimSGR("one\ntwo\nthree", "#777777")
		stripped := ansi.Strip(got)
		if stripped != "one\ntwo\nthree" {
			t.Fatalf("expected three lines preserved, got %q", stripped)
		}
	})
}

func TestOverlay(t *testing.T) {
	t.Parallel()

	t.Run("modal centered on plain base", func(t *testing.T) {
		t.Parallel()
		base := strings.Repeat("....................\n", 7)
		base = strings.TrimRight(base, "\n") // 7 rows of 20 dots
		modal := "AAAA\nBBBB\nCCCC"          // 3×4
		got := Overlay(base, modal, 20, 7)
		// Center: left = (20-4)/2 = 8; top = (7-3)/2 = 2.
		lines := strings.Split(got, "\n")
		if len(lines) != 7 {
			t.Fatalf("expected 7 rows, got %d", len(lines))
		}
		if ansi.Strip(lines[0]) != strings.Repeat(".", 20) {
			t.Fatalf("row 0 should be untouched dots, got %q", ansi.Strip(lines[0]))
		}
		if ansi.Strip(lines[2]) != "........AAAA........" {
			t.Fatalf("row 2 mismatch, got %q", ansi.Strip(lines[2]))
		}
		if ansi.Strip(lines[4]) != "........CCCC........" {
			t.Fatalf("row 4 mismatch, got %q", ansi.Strip(lines[4]))
		}
		if ansi.Strip(lines[6]) != strings.Repeat(".", 20) {
			t.Fatalf("row 6 should be untouched dots, got %q", ansi.Strip(lines[6]))
		}
	})

	t.Run("empty modal returns base unchanged", func(t *testing.T) {
		t.Parallel()
		base := "abc\ndef"
		if got := Overlay(base, "", 10, 5); got != base {
			t.Fatalf("expected base unchanged, got %q", got)
		}
	})

	t.Run("modal larger than viewport clips", func(t *testing.T) {
		t.Parallel()
		base := strings.Repeat(".", 10)
		modal := "AAAAAAAAAAAAAAAA\nBBBBBBBBBBBBBBBB\nCCCCCCCCCCCCCCCC" // 3×16
		got := Overlay(base, modal, 10, 2)
		lines := strings.Split(got, "\n")
		if len(lines) != 2 {
			t.Fatalf("expected 2 rows after clip, got %d", len(lines))
		}
		// modalW clamped to 10, left = 0; modalH clamped to 2; top = 0.
		if ansi.Strip(lines[0]) != "AAAAAAAAAA" {
			t.Fatalf("row 0 expected clipped A's, got %q", ansi.Strip(lines[0]))
		}
		if ansi.Strip(lines[1]) != "BBBBBBBBBB" {
			t.Fatalf("row 1 expected clipped B's, got %q", ansi.Strip(lines[1]))
		}
	})

	t.Run("base shorter than viewport pads with empty rows", func(t *testing.T) {
		t.Parallel()
		base := "abc"
		modal := "X"
		got := Overlay(base, modal, 5, 3)
		lines := strings.Split(got, "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 rows, got %d", len(lines))
		}
		// Modal: 1×1, top = (3-1)/2 = 1, left = (5-1)/2 = 2.
		if ansi.Strip(lines[1]) != "  X  " {
			t.Fatalf("modal row expected '  X  ', got %q", ansi.Strip(lines[1]))
		}
	})

	t.Run("styled cells outside modal region survive", func(t *testing.T) {
		t.Parallel()
		r := truecolorRenderer()
		pink := r.NewStyle().Foreground(lipgloss.Color("#FF7DB0"))
		baseLine := pink.Render("LLLL") + "...." + pink.Render("RRRR") // 12 cells
		modal := "MM"                                                  // 1×2
		got := Overlay(baseLine, modal, 12, 1)
		// left = (12-2)/2 = 5, modal occupies cells [5,7). The pink
		// LLLL on cells [0,4) and pink RRRR on cells [8,12) must still
		// be present in the output as SGR-decorated runes.
		if !strings.Contains(got, "LLLL") {
			t.Fatalf("expected LLLL preserved, got %q", got)
		}
		if !strings.Contains(got, "RRRR") {
			t.Fatalf("expected RRRR preserved, got %q", got)
		}
		// The pink color SGR must still be present somewhere too —
		// stripping it entirely would be a regression.
		if !strings.Contains(got, "125;176") {
			t.Fatalf("expected pink SGR preserved, got %q", got)
		}
	})
}
