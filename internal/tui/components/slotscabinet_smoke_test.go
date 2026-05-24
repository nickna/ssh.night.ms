package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderSlotsCabinet_Dimensions verifies the cabinet renders to
// exactly SlotsCabinetWidth × SlotsCabinetHeight cells regardless of
// state (idle, mid-spin, win flash, with coins). A drift here would
// break centering math in the screen and produce ragged borders.
func TestRenderSlotsCabinet_Dimensions(t *testing.T) {
	cases := []struct {
		name  string
		state SlotsCabinetState
	}{
		{name: "idle", state: SlotsCabinetState{
			Reels: [3]SlotSymbolID{SlotCherry, SlotLemon, SlotPlum},
		}},
		{name: "spinning", state: SlotsCabinetState{
			Spinning:     [3]bool{true, true, true},
			ScrollOffset: [3]int{0, 7, 14},
		}},
		{name: "jackpot_with_coins", state: SlotsCabinetState{
			Reels:      [3]SlotSymbolID{SlotSeven, SlotSeven, SlotSeven},
			WinTier:    SlotsWinJackpot,
			FlashFrame: 5,
			Coins: []SlotsCoin{
				{X: 5, Y: 11}, {X: 10, Y: 8}, {X: 20, Y: 6},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderSlotsCabinet(tc.state)
			lines := strings.Split(out, "\n")
			if len(lines) != SlotsCabinetHeight {
				t.Fatalf("got %d lines, want %d", len(lines), SlotsCabinetHeight)
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != SlotsCabinetWidth {
					t.Errorf("line %d width = %d, want %d (line: %q)", i, w, SlotsCabinetWidth, ansi.Strip(l))
				}
			}
		})
	}
}

// TestRenderSlotsCabinet_BorderGlyphs checks the cabinet draws the
// classic ╔═...═╗ outer frame at the expected coordinates. Acts as a
// smoke test against PaintStaticChrome regressing the housing layout.
func TestRenderSlotsCabinet_BorderGlyphs(t *testing.T) {
	out := RenderSlotsCabinet(SlotsCabinetState{
		Reels: [3]SlotSymbolID{SlotCherry, SlotLemon, SlotPlum},
	})
	lines := strings.Split(out, "\n")
	stripped := make([]string, len(lines))
	for i, l := range lines {
		stripped[i] = ansi.Strip(l)
	}
	checks := []struct {
		row int
		col int
		got rune
	}{
		{0, 0, '╔'}, {0, SlotsCabinetWidth - 1, '╗'},
		{SlotsCabinetHeight - 1, 0, '╚'},
		{SlotsCabinetHeight - 1, SlotsCabinetWidth - 1, '╝'},
		{2, 0, '╠'}, {2, SlotsCabinetWidth - 1, '╣'},
	}
	for _, c := range checks {
		runes := []rune(stripped[c.row])
		if c.col >= len(runes) {
			t.Errorf("row %d shorter than expected, len=%d", c.row, len(runes))
			continue
		}
		if runes[c.col] != c.got {
			t.Errorf("row %d col %d = %q, want %q", c.row, c.col, runes[c.col], c.got)
		}
	}
}
