package components

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/tui/nav"
)

// fixture: three items so wrap behaviour and neighbour hit-tests both have
// something distinct to compare against.
func newTestCarousel() *Carousel {
	items := []CarouselItem{
		{Title: "Alpha", Hotkey: 'a', Destination: nav.DestChat},
		{Title: "Beta", Hotkey: 'b', Destination: nav.DestBoards},
		{Title: "Gamma", Hotkey: 'g', Destination: nav.DestProfile},
	}
	c := NewCarousel(items)
	// Prime lastWidth so geometry is computable; View also exercises the
	// real layout code path and matches what the screen would produce.
	_ = c.View(80)
	return c
}

// At width=80, selected=0 is centered: LeftX=(80-20)/2=30, Width=20, so the
// hit area is X ∈ [30,50), Y ∈ [0,7). Neighbour +1 sits to its right at
// LeftX = 30+20+2 = 52, Width=14, Y ∈ [2,7).

func TestCarousel_MouseClick_SelectedActivates(t *testing.T) {
	t.Parallel()
	c := newTestCarousel()
	cmd, dest := c.Update(tea.MouseMsg{
		X: 40, Y: 3, // center of selected card
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestChat {
		t.Fatalf("expected DestChat from clicking selected card, got %v", dest)
	}
	if cmd != nil {
		t.Fatalf("activation should not start an animation; got non-nil cmd")
	}
}

func TestCarousel_MouseClick_NeighbourJumps(t *testing.T) {
	t.Parallel()
	c := newTestCarousel()
	cmd, dest := c.Update(tea.MouseMsg{
		X: 58, Y: 4, // inside the +1 neighbour card
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestNone {
		t.Fatalf("neighbour click should not return a destination; got %v", dest)
	}
	if cmd == nil {
		t.Fatalf("neighbour click should kick off an animation tick")
	}
	if c.selected != 1 {
		t.Fatalf("expected selected=1 after jumping to neighbour, got %d", c.selected)
	}
}

func TestCarousel_MouseClick_OutsideIgnored(t *testing.T) {
	t.Parallel()
	c := newTestCarousel()

	// Far left of the screen, before the leftmost card.
	cmd, dest := c.Update(tea.MouseMsg{
		X: 2, Y: 3,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestNone || cmd != nil {
		t.Fatalf("click outside cards should be a no-op; got dest=%v cmd=%v", dest, cmd != nil)
	}

	// Below the card row.
	cmd, dest = c.Update(tea.MouseMsg{
		X: 40, Y: 12,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestNone || cmd != nil {
		t.Fatalf("click below the card row should be a no-op; got dest=%v cmd=%v", dest, cmd != nil)
	}
}

func TestCarousel_MouseClick_PressIgnored(t *testing.T) {
	t.Parallel()
	c := newTestCarousel()
	// Press without release should not trigger activation — Release is the
	// commit point so users can abort a click by moving off the card first.
	cmd, dest := c.Update(tea.MouseMsg{
		X: 40, Y: 3,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	if dest != nav.DestNone || cmd != nil {
		t.Fatalf("Press alone should not activate; got dest=%v cmd=%v", dest, cmd != nil)
	}
}

func TestCarousel_MouseWheel_MovesSelection(t *testing.T) {
	t.Parallel()
	c := newTestCarousel()

	// Wheel down → next card.
	if _, dest := c.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown}); dest != nav.DestNone {
		t.Fatalf("wheel-down should not return a destination, got %v", dest)
	}
	if c.selected != 1 {
		t.Fatalf("wheel-down should move selection to 1, got %d", c.selected)
	}

	// Wheel up → previous card (wraps back to 0).
	c.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if c.selected != 0 {
		t.Fatalf("wheel-up should move selection back to 0, got %d", c.selected)
	}

	// Wheel up from 0 wraps to the last item.
	c.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if c.selected != 2 {
		t.Fatalf("wheel-up from 0 should wrap to 2, got %d", c.selected)
	}
}

func TestCarousel_SetViewport_TranslatesCoords(t *testing.T) {
	t.Parallel()
	c := newTestCarousel()
	c.SetViewport(0, 5)
	// Same click point as the selected-activates test, shifted down by the
	// viewport origin: Y=8 with viewportY=5 → relY=3, inside the card.
	_, dest := c.Update(tea.MouseMsg{
		X: 40, Y: 8,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestChat {
		t.Fatalf("viewport-translated click should activate selected; got %v", dest)
	}

	// A click at Y=3 in screen-space now sits ABOVE the carousel (relY=-2)
	// and should be ignored.
	_, dest = c.Update(tea.MouseMsg{
		X: 40, Y: 3,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestNone {
		t.Fatalf("click above translated viewport should be ignored; got %v", dest)
	}
}

func TestCarousel_MouseClick_NoView_NoOp(t *testing.T) {
	t.Parallel()
	// No View call → lastWidth == 0; clicks should be safe no-ops rather
	// than panicking on the unset geometry.
	c := NewCarousel([]CarouselItem{
		{Title: "Solo", Hotkey: 's', Destination: nav.DestChat},
	})
	cmd, dest := c.Update(tea.MouseMsg{
		X: 40, Y: 3,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
	})
	if dest != nav.DestNone || cmd != nil {
		t.Fatalf("click before first View should be a no-op; got dest=%v cmd=%v", dest, cmd != nil)
	}
}
