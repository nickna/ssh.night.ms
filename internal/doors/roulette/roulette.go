// Package roulette implements an American (38-pocket) roulette wheel. The
// engine is pure data + helpers — no I/O, no animation, no networking.
// Mirrors the layout of internal/doors/slots/slots.go: small public surface,
// table-driven properties, single-call Evaluate for bet outcomes. The
// multiplayer coordinator (internal/doors/roulette/multiplayer) wraps this
// with the shared-table state machine; the TUI screen consumes both.
package roulette

// Color is the visual color of a pocket. Pockets 0 and 00 are Green; the
// remaining 36 numbers are split into Red and Black by the classic
// American wheel coloring.
type Color uint8

const (
	Green Color = iota
	Red
	Black
)

func (c Color) String() string {
	switch c {
	case Red:
		return "red"
	case Black:
		return "black"
	case Green:
		return "green"
	}
	return "?"
}

// Pocket is one of the 38 pockets on an American wheel. The numeric pockets
// 0..36 use indices 0..36 directly so most lookups are array reads; 00 lives
// at index 37 (Pocket00) since "double-zero" has no clean numeric value.
type Pocket uint8

// Pocket00 is the "double-zero" pocket — index 37 by convention so we can
// keep the numeric pockets aligned with their face values.
const Pocket00 Pocket = 37

// PocketCount is the total number of pockets on the wheel.
const PocketCount = 38

// Number renders the pocket's face string ("0", "00", "1".."36").
func (p Pocket) Number() string {
	if p == Pocket00 {
		return "00"
	}
	if int(p) >= 0 && int(p) <= 36 {
		return pocketNumberStr[p]
	}
	return "?"
}

// IsZero reports whether the pocket is 0 or 00 — i.e. all outside bets lose.
func (p Pocket) IsZero() bool { return p == 0 || p == Pocket00 }

// Color returns the visual color of the pocket.
func (p Pocket) Color() Color {
	if p.IsZero() {
		return Green
	}
	if isRed[p] {
		return Red
	}
	return Black
}

// IsRed reports whether the pocket is a red pocket. Zeros are green, so this
// is shorthand for Color() == Red.
func (p Pocket) IsRed() bool { return p.Color() == Red }

// IsBlack reports whether the pocket is a black pocket. Zeros are green, so
// this is shorthand for Color() == Black.
func (p Pocket) IsBlack() bool { return p.Color() == Black }

// IsEven reports whether the pocket is an even non-zero pocket. Zeros never
// satisfy any outside bet, including Even.
func (p Pocket) IsEven() bool {
	if p.IsZero() {
		return false
	}
	return p%2 == 0
}

// IsOdd reports whether the pocket is an odd pocket. Zeros never satisfy any
// outside bet, including Odd.
func (p Pocket) IsOdd() bool {
	if p.IsZero() {
		return false
	}
	return p%2 == 1
}

// IsLow reports whether the pocket is in the low half (1..18).
func (p Pocket) IsLow() bool {
	if p.IsZero() {
		return false
	}
	return p >= 1 && p <= 18
}

// IsHigh reports whether the pocket is in the high half (19..36).
func (p Pocket) IsHigh() bool {
	if p.IsZero() {
		return false
	}
	return p >= 19 && p <= 36
}

// Dozen returns 1, 2, or 3 for pockets 1-12, 13-24, 25-36 respectively; 0
// for the zeros.
func (p Pocket) Dozen() int {
	switch {
	case p.IsZero():
		return 0
	case p <= 12:
		return 1
	case p <= 24:
		return 2
	default:
		return 3
	}
}

// Column returns 1, 2, or 3 for the table's three columns; 0 for the zeros.
// Column 1 holds {1,4,7,...,34}; Column 2 holds {2,5,8,...,35}; Column 3 holds
// {3,6,9,...,36}. Derived from the standard felt layout, where columns run
// down the table parallel to the number grid.
func (p Pocket) Column() int {
	if p.IsZero() {
		return 0
	}
	switch p % 3 {
	case 1:
		return 1
	case 2:
		return 2
	default: // 0 → divisible by 3 → column 3
		return 3
	}
}

// pocketNumberStr is the printable face value cache, indexed by pocket. Index
// 37 (Pocket00) is filled with "00" so Number() can be a single array read.
var pocketNumberStr = func() [PocketCount]string {
	var arr [PocketCount]string
	for i := 0; i <= 36; i++ {
		arr[i] = itoa(i)
	}
	arr[Pocket00] = "00"
	return arr
}()

// isRed tracks which numeric pockets are red on the American wheel. The 18
// red numbers come from the canonical layout (the rest of 1..36 are black,
// the zeros are green and never satisfy this map).
var isRed = func() [PocketCount]bool {
	var arr [PocketCount]bool
	for _, n := range [...]int{1, 3, 5, 7, 9, 12, 14, 16, 18, 19, 21, 23, 25, 27, 30, 32, 34, 36} {
		arr[n] = true
	}
	return arr
}()

// itoa is a tiny zero-allocation int → string for the 0..36 face values used
// by pocketNumberStr. strconv works fine, but the closure runs once at init
// so the inline form keeps the file dependency-free.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// RibbonOrder is the canonical clockwise ordering of pockets on an American
// roulette wheel, starting at 0. The race-track ribbon animation scrolls
// through this slice so the rendered strip matches what a player would see
// on a real wheel.
var RibbonOrder = [PocketCount]Pocket{
	0, 28, 9, 26, 30, 11, 7, 20, 32, 17,
	5, 22, 34, 15, 3, 24, 36, 13, 1, Pocket00,
	27, 10, 25, 29, 12, 8, 19, 31, 18, 6,
	21, 33, 16, 4, 23, 35, 14, 2,
}

// RibbonIndex returns the index of pocket p in RibbonOrder. Used by the
// ribbon renderer to position the ball-marker. -1 if p is somehow out of
// range (defensive — every well-formed Pocket appears exactly once).
func RibbonIndex(p Pocket) int {
	for i, q := range RibbonOrder {
		if q == p {
			return i
		}
	}
	return -1
}
