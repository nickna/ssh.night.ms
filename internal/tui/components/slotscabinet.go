// Package components — slotscabinet.go draws the full slot-machine
// cabinet body (marquee, three reel housings, side handle, coin tray)
// as a single multi-line string. Pure function: the screen owns the
// SlotsCabinetState and re-passes it each frame; the renderer holds
// no state across calls.
//
// Layout (38 cols × 13 rows, matching the .NET SlotsCabinetView):
//
//	row  0  ╔══...══╗                          outer border
//	row  1  ║   ★ NIGHT.MS SLOTS ★    ║        marquee
//	row  2  ╠══...══╣                          separator
//	row  3  ║                          ║        interior
//	row  4  ║   ╔═════╗ ╔═════╗ ╔═════╗ ╔══╗ ║  housing + handle tops
//	row  5  ║   ║ S1  ║ ║ S2  ║ ║ S3  ║ ║██║ ║  sprite row 1
//	row  6  ║   ║ S1  ║ ║ S2  ║ ║ S3  ║ ║██║ ║  sprite row 2
//	row  7  ║   ║ S1  ║ ║ S2  ║ ║ S3  ║ ║██║ ║  sprite row 3
//	row  8  ║   ╚═════╝ ╚═════╝ ╚═════╝ ╚══╝ ║  housing + handle bottoms
//	row  9  ║                          ║        interior
//	row 10  ╠══...══╣                          separator
//	row 11  ║      $ coin tray         ║        coins float up through here
//	row 12  ╚══...══╝                          outer border
package components

import (
	"image/color"

	"github.com/nickna/ssh.night.ms/internal/tui/art"
)

// SlotsCabinetWidth and SlotsCabinetHeight are the cabinet's exact cell
// dimensions. Callers reserve this space in their layout (or pad around
// it). Constants match SlotsCabinetView in the .NET project.
const (
	SlotsCabinetWidth  = 38
	SlotsCabinetHeight = 13
)

// Reel housing inner top-left column (the cell where the sprite's (0,0)
// lands). The inner row is fixed at slotsReelInnerY for all three reels.
var slotsReelInnerX = [3]int{4, 13, 22}

const slotsReelInnerY = 5

// SlotsWinTier classifies a win for the border-flash + coin-shower
// animations. None disables the flash; Jackpot uses a faster red↔gold
// cycle, Normal uses a slower gold↔white cycle.
type SlotsWinTier int

const (
	SlotsWinNone SlotsWinTier = iota
	SlotsWinNormal
	SlotsWinJackpot
)

// SlotsCabinetState is everything the renderer needs to paint one frame.
// Screens build this from their own animation timers and pass it in. No
// pointers / mutability so a state value can be safely copied between
// tick handlers.
type SlotsCabinetState struct {
	// Reels[i] is the locked symbol for reel i. Ignored when Spinning[i]
	// is true — the renderer pulls from the scrolling strip instead.
	Reels [3]SlotSymbolID

	// Spinning[i] true means reel i shows a moving strip rather than a
	// locked sprite. ScrollOffset[i] is the strip's current vertical
	// offset in rows; the renderer modulos against the virtual strip
	// length so callers can monotonically increment without wrapping.
	Spinning     [3]bool
	ScrollOffset [3]int

	// WinTier + FlashFrame drive the outer border color cycle. FlashFrame
	// is monotonically increasing while a win is active; the renderer
	// derives the per-frame color from its parity.
	WinTier    SlotsWinTier
	FlashFrame int

	// Coins is the current coin-burst state — each entry is one '$'
	// glyph drifting upward through the coin tray + interior. Pass an
	// empty slice when no win is animating.
	Coins []SlotsCoin
}

// SlotsCoin is one floating '$' inside the cabinet. (X, Y) are cell
// coordinates inside the cabinet (0..Width-1, 0..Height-1). Coins
// outside the interior region are clipped at render time.
type SlotsCoin struct {
	X, Y int
}

// cycleSymbols is the visible order during a spin — high-contrast
// symbols are spaced apart so the eye sees the reel "moving" even at
// slow scroll speeds. Lemon and Orange are deliberately not adjacent
// because they look similar at a glance.
var cycleSymbols = [...]SlotSymbolID{
	SlotCherry,
	SlotLemon,
	SlotPlum,
	SlotBell,
	SlotOrange,
	SlotBar,
	SlotSeven,
}

// RenderSlotsCabinet paints the cabinet body for one frame and returns
// it as a ready-to-print string (SGR runs batched per row of same-
// styled cells — see RenderCellGrid).
func RenderSlotsCabinet(state SlotsCabinetState) string {
	g := art.NewCellGrid(SlotsCabinetWidth, SlotsCabinetHeight)
	paintCabinetChrome(g)
	paintCabinetMarquee(g)
	for i := 0; i < 3; i++ {
		paintCabinetReel(g, i, state)
	}
	paintCabinetCoins(g, state.Coins)
	// Border flash repaints the existing border glyphs in a different
	// color; runs after the rest so coin / sprite cells are untouched.
	if state.WinTier != SlotsWinNone {
		recolorBorder(g, borderFlashColor(state.WinTier, state.FlashFrame))
	}
	return RenderCellGrid(g)
}

// paintCabinetChrome draws the outer border, separators, three reel
// housings, and the side handle (block-character body in gold) in the
// default bright-white chrome.
func paintCabinetChrome(g *art.CellGrid) {
	fg := SlotPalette.BrightWhite

	// Outer border.
	setCell(g, 0, 0, '╔', fg, true)
	setCell(g, SlotsCabinetWidth-1, 0, '╗', fg, true)
	setCell(g, 0, SlotsCabinetHeight-1, '╚', fg, true)
	setCell(g, SlotsCabinetWidth-1, SlotsCabinetHeight-1, '╝', fg, true)
	for x := 1; x < SlotsCabinetWidth-1; x++ {
		setCell(g, x, 0, '═', fg, true)
		setCell(g, x, SlotsCabinetHeight-1, '═', fg, true)
	}
	for y := 1; y < SlotsCabinetHeight-1; y++ {
		setCell(g, 0, y, '║', fg, true)
		setCell(g, SlotsCabinetWidth-1, y, '║', fg, true)
	}

	// Marquee separator (row 2) + coin-tray separator (row 10).
	setCell(g, 0, 2, '╠', fg, true)
	setCell(g, SlotsCabinetWidth-1, 2, '╣', fg, true)
	setCell(g, 0, 10, '╠', fg, true)
	setCell(g, SlotsCabinetWidth-1, 10, '╣', fg, true)
	for x := 1; x < SlotsCabinetWidth-1; x++ {
		setCell(g, x, 2, '═', fg, true)
		setCell(g, x, 10, '═', fg, true)
	}

	// Three reel housings — each is a 7×5 outer box framing a 5×3 sprite.
	for _, leftCol := range slotsReelInnerX {
		paintHousing(g, leftCol-1, 4, SlotSpriteWidth, SlotSpriteHeight, fg)
	}

	// Side handle: a 4-col × 5-row housing with a gold "knob" body.
	paintHousing(g, 31, 4, 2, 3, fg)
	for y := 5; y <= 7; y++ {
		setCell(g, 32, y, '█', SlotPalette.Gold, true)
		setCell(g, 33, y, '█', SlotPalette.Gold, true)
	}
}

// paintHousing draws a box of inner size (innerCols × innerRows) with
// its top-left CORNER at (leftCol, topRow). The corners and edges are
// drawn in the chrome color; the interior is left untouched (the reel
// painter fills it).
func paintHousing(g *art.CellGrid, leftCol, topRow, innerCols, innerRows int, fg *color.NRGBA) {
	rightCol := leftCol + innerCols + 1
	bottomRow := topRow + innerRows + 1
	setCell(g, leftCol, topRow, '╔', fg, true)
	setCell(g, rightCol, topRow, '╗', fg, true)
	setCell(g, leftCol, bottomRow, '╚', fg, true)
	setCell(g, rightCol, bottomRow, '╝', fg, true)
	for x := leftCol + 1; x < rightCol; x++ {
		setCell(g, x, topRow, '═', fg, true)
		setCell(g, x, bottomRow, '═', fg, true)
	}
	for y := topRow + 1; y < bottomRow; y++ {
		setCell(g, leftCol, y, '║', fg, true)
		setCell(g, rightCol, y, '║', fg, true)
	}
}

func paintCabinetMarquee(g *art.CellGrid) {
	const text = "★ NIGHT.MS SLOTS ★"
	runes := []rune(text)
	startX := (SlotsCabinetWidth - len(runes)) / 2
	for i, r := range runes {
		setCell(g, startX+i, 1, r, SlotPalette.Gold, true)
	}
}

// paintCabinetReel paints reel index `reel` based on the supplied state.
// When spinning, the renderer paints a 3-row window into a virtual strip
// of stacked sprites at ScrollOffset[reel] — produces the "scrolling
// reel" illusion. When locked, paints the single sprite at Reels[reel].
func paintCabinetReel(g *art.CellGrid, reel int, state SlotsCabinetState) {
	innerX := slotsReelInnerX[reel]
	if state.Spinning[reel] {
		offset := state.ScrollOffset[reel]
		for y := 0; y < SlotSpriteHeight; y++ {
			stripRow := offset + y
			symIdx := positiveMod(stripRow/SlotSpriteHeight, len(cycleSymbols))
			spriteRow := positiveMod(stripRow, SlotSpriteHeight)
			sym := cycleSymbols[symIdx]
			for x := 0; x < SlotSpriteWidth; x++ {
				cell := SlotSpriteAt(sym, x, spriteRow)
				if c := g.At(slotsReelInnerY+y, innerX+x); c != nil {
					*c = cell
				}
			}
		}
		return
	}
	sym := state.Reels[reel]
	for y := 0; y < SlotSpriteHeight; y++ {
		for x := 0; x < SlotSpriteWidth; x++ {
			cell := SlotSpriteAt(sym, x, y)
			if c := g.At(slotsReelInnerY+y, innerX+x); c != nil {
				*c = cell
			}
		}
	}
}

// paintCabinetCoins paints '$' glyphs at each coin position, clipped to
// the interior cells (avoids overwriting the border). Coins past the
// top of the interior are silently skipped — the screen drops them on
// its own tick.
func paintCabinetCoins(g *art.CellGrid, coins []SlotsCoin) {
	for _, c := range coins {
		if c.X <= 0 || c.X >= SlotsCabinetWidth-1 {
			continue
		}
		if c.Y < 3 || c.Y > 11 {
			continue
		}
		// Skip cells already filled by a non-blank sprite glyph — coins
		// only paint over interior background spaces and the coin-tray
		// row. A `▌`, `█`, or letter inside a reel housing should not
		// disappear behind a floating coin.
		if cell := g.At(c.Y, c.X); cell != nil && cell.Rune != ' ' {
			continue
		}
		setCell(g, c.X, c.Y, '$', SlotPalette.CoinGold, true)
	}
}

// recolorBorder repaints the outer border + both separator rows in the
// supplied color, leaving the existing glyphs intact. Only the FG and
// Bold attributes change.
func recolorBorder(g *art.CellGrid, fg *color.NRGBA) {
	rows := []int{0, 2, 10, SlotsCabinetHeight - 1}
	for _, y := range rows {
		for x := 0; x < SlotsCabinetWidth; x++ {
			if cell := g.At(y, x); cell != nil {
				cell.Fg = fg
				cell.Bold = true
			}
		}
	}
	for y := 1; y < SlotsCabinetHeight-1; y++ {
		if cell := g.At(y, 0); cell != nil {
			cell.Fg = fg
			cell.Bold = true
		}
		if cell := g.At(y, SlotsCabinetWidth-1); cell != nil {
			cell.Fg = fg
			cell.Bold = true
		}
	}
}

// borderFlashColor chooses the per-frame outer-border color for a win.
// Jackpot cycles every frame between red and gold (urgent); Normal
// cycles every other frame between gold and bright-white (celebratory).
func borderFlashColor(tier SlotsWinTier, frame int) *color.NRGBA {
	switch tier {
	case SlotsWinJackpot:
		if frame%2 == 0 {
			return SlotPalette.SevenRed
		}
		return SlotPalette.Gold
	case SlotsWinNormal:
		if (frame/2)%2 == 0 {
			return SlotPalette.Gold
		}
		return SlotPalette.BrightWhite
	default:
		return SlotPalette.BrightWhite
	}
}

func setCell(g *art.CellGrid, x, y int, r rune, fg *color.NRGBA, bold bool) {
	if c := g.At(y, x); c != nil {
		*c = art.Cell{Rune: r, Fg: fg, Bold: bold}
	}
}

func positiveMod(a, b int) int {
	if b <= 0 {
		return 0
	}
	r := a % b
	if r < 0 {
		r += b
	}
	return r
}
