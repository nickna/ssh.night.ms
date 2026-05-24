package components

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderSlotsCabinet_Visual is a smoke-only dump — `go test -v` prints
// the cabinet so a human can eyeball it. No assertions; the dimensions
// test in this directory covers the structural checks.
func TestRenderSlotsCabinet_Visual(t *testing.T) {
	t.Log("\n--- idle ---")
	t.Log("\n" + ansi.Strip(RenderSlotsCabinet(SlotsCabinetState{
		Reels: [3]SlotSymbolID{SlotCherry, SlotLemon, SlotPlum},
	})))

	t.Log("\n--- jackpot ---")
	t.Log("\n" + ansi.Strip(RenderSlotsCabinet(SlotsCabinetState{
		Reels:      [3]SlotSymbolID{SlotSeven, SlotSeven, SlotSeven},
		WinTier:    SlotsWinJackpot,
		FlashFrame: 0,
		Coins: []SlotsCoin{
			{X: 5, Y: 11}, {X: 12, Y: 9}, {X: 20, Y: 7}, {X: 27, Y: 5},
		},
	})))

	t.Log("\n--- spinning ---")
	t.Log("\n" + ansi.Strip(RenderSlotsCabinet(SlotsCabinetState{
		Spinning:     [3]bool{true, true, true},
		ScrollOffset: [3]int{0, 3, 7},
	})))
}
