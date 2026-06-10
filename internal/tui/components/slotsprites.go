// Package components — slotsprites.go holds the 5×3 colored sprites for
// the seven slot symbols, plus the cabinet palette. Sprites are baked at
// package-init time into art.Cell grids so the cabinet view never
// allocates a sprite per frame.
//
// Glyphs are restricted to ASCII + the half-block / box-drawing ranges
// already used by the art pipeline (▀ ▄ █ ▌ ▐ ╔ ═ ╗ ║ ╚ ╝ ╠ ╣ ★). All are
// single-column on every SSH client we target, so each sprite is exactly
// 5 cells wide regardless of the terminal's emoji-width policy.
package components

import (
	"image/color"

	"github.com/nickna/ssh.night.ms/internal/tui/art"
)

// SlotSpriteWidth and SlotSpriteHeight pin the per-symbol sprite size.
// The cabinet view + reel housing dimensions assume these values.
const (
	SlotSpriteWidth  = 5
	SlotSpriteHeight = 3
)

// SlotPalette is the shared color set for the slot cabinet — kept inside
// this package because the colors are tightly coupled to the sprite
// strokes (a Bell sprite in red would just look wrong). Hex literals so
// the slot module stays standalone vs theme.go.
var SlotPalette = struct {
	Black       *color.NRGBA
	BrightWhite *color.NRGBA
	Gold        *color.NRGBA
	BarGold     *color.NRGBA
	SevenRed    *color.NRGBA
	BellYellow  *color.NRGBA
	LemonYellow *color.NRGBA
	OrangeFruit *color.NRGBA
	PlumMagenta *color.NRGBA
	CherryRed   *color.NRGBA
	CherryStem  *color.NRGBA
	DimGray     *color.NRGBA
	CoinGold    *color.NRGBA
}{
	Black:       art.Rgb(0, 0, 0),
	BrightWhite: art.Rgb(220, 220, 220),
	Gold:        art.Rgb(255, 215, 0),
	BarGold:     art.Rgb(255, 195, 40),
	SevenRed:    art.Rgb(255, 40, 50),
	BellYellow:  art.Rgb(255, 230, 90),
	LemonYellow: art.Rgb(240, 240, 95),
	OrangeFruit: art.Rgb(255, 140, 40),
	PlumMagenta: art.Rgb(200, 80, 200),
	CherryRed:   art.Rgb(220, 40, 60),
	CherryStem:  art.Rgb(60, 180, 60),
	DimGray:     art.Rgb(95, 95, 95),
	CoinGold:    art.Rgb(255, 215, 0),
}

// SlotSymbolID indexes the sprite table. Values match the ordering in
// internal/doors/slots so the screen can pass slots.Symbol straight in
// after a uint cast — no translation layer needed.
type SlotSymbolID int

const (
	SlotCherry SlotSymbolID = iota
	SlotLemon
	SlotOrange
	SlotPlum
	SlotBell
	SlotBar
	SlotSeven
)

// SlotSymbolCount is the number of distinct symbols. Reel-spin cycling
// uses this as the modulus.
const SlotSymbolCount = 7

// Single-color sprite patterns — '.' = empty, anything else paints the
// sprite's primary color on the cabinet background. Half-block runes
// carry their own shape, so the fg color is what the player perceives.
// Cherry is two-color and is built by a dedicated helper.
var slotPatterns = [SlotSymbolCount][SlotSpriteHeight]string{
	SlotCherry: {
		"..g..",
		"rr.rr",
		"rr.rr",
	},
	SlotLemon: {
		".▄▀▄.",
		"▌...▐",
		".▀▄▀.",
	},
	SlotOrange: {
		".▄▀▄.",
		"▐███▌",
		".▀▄▀.",
	},
	SlotPlum: {
		".▄█▄.",
		"▐███▌",
		".▀█▀.",
	},
	SlotBell: {
		".▄█▄.",
		"█████",
		".▀█▀.",
	},
	SlotBar: {
		"█████",
		" BAR ",
		"█████",
	},
	SlotSeven: {
		"▄▄▄▄▄",
		"...▄▀",
		".▄▀..",
	},
}

// Per-symbol primary color + bold flag. Cherry has no primary color (it
// uses red + green from the dedicated builder).
var slotColors = [SlotSymbolCount]struct {
	FG   *color.NRGBA
	Bold bool
}{
	SlotCherry: {nil, true}, // unused — BuildCherrySprite paints directly
	SlotLemon:  {SlotPalette.LemonYellow, false},
	SlotOrange: {SlotPalette.OrangeFruit, true},
	SlotPlum:   {SlotPalette.PlumMagenta, false},
	SlotBell:   {SlotPalette.BellYellow, true},
	SlotBar:    {SlotPalette.BarGold, true},
	SlotSeven:  {SlotPalette.SevenRed, true},
}

// slotSprites is the baked sprite table. Index by SlotSymbolID. Each
// entry is a SlotSpriteWidth × SlotSpriteHeight cell grid that the
// cabinet view blits straight into its reel housing.
var slotSprites [SlotSymbolCount][SlotSpriteHeight][SlotSpriteWidth]art.Cell

func init() {
	for sym := SlotSymbolID(0); sym < SlotSymbolCount; sym++ {
		if sym == SlotCherry {
			slotSprites[sym] = buildCherrySprite()
			continue
		}
		slotSprites[sym] = buildSingleColorSprite(slotPatterns[sym], slotColors[sym].FG, slotColors[sym].Bold)
	}
}

func buildSingleColorSprite(pattern [SlotSpriteHeight]string, fg *color.NRGBA, bold bool) [SlotSpriteHeight][SlotSpriteWidth]art.Cell {
	var out [SlotSpriteHeight][SlotSpriteWidth]art.Cell
	for y := 0; y < SlotSpriteHeight; y++ {
		runes := []rune(pattern[y])
		for x := 0; x < SlotSpriteWidth; x++ {
			if x >= len(runes) || runes[x] == '.' {
				out[y][x] = art.Cell{Rune: ' '}
				continue
			}
			out[y][x] = art.Cell{Rune: runes[x], Fg: fg, Bold: bold}
		}
	}
	return out
}

// buildCherrySprite uses 'r' (red █) + 'g' (green ▌ stem) so the fruit
// reads as red dots with a tiny green stem above. '.' is empty.
func buildCherrySprite() [SlotSpriteHeight][SlotSpriteWidth]art.Cell {
	var out [SlotSpriteHeight][SlotSpriteWidth]art.Cell
	for y := 0; y < SlotSpriteHeight; y++ {
		runes := []rune(slotPatterns[SlotCherry][y])
		for x := 0; x < SlotSpriteWidth; x++ {
			if x >= len(runes) {
				out[y][x] = art.Cell{Rune: ' '}
				continue
			}
			switch runes[x] {
			case 'r':
				out[y][x] = art.Cell{Rune: '█', Fg: SlotPalette.CherryRed, Bold: true}
			case 'g':
				out[y][x] = art.Cell{Rune: '▌', Fg: SlotPalette.CherryStem, Bold: true}
			default:
				out[y][x] = art.Cell{Rune: ' '}
			}
		}
	}
	return out
}

// SlotSpriteAt returns the cell at (x, y) of the named symbol. Out-of-
// range coordinates return a blank cell. The cabinet view uses this so
// scrolling math can index past the natural sprite bounds without
// bounds-checking at every step.
func SlotSpriteAt(sym SlotSymbolID, x, y int) art.Cell {
	if sym < 0 || sym >= SlotSymbolCount {
		return art.Cell{Rune: ' '}
	}
	if x < 0 || x >= SlotSpriteWidth || y < 0 || y >= SlotSpriteHeight {
		return art.Cell{Rune: ' '}
	}
	return slotSprites[sym][y][x]
}
