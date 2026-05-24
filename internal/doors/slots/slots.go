// Package slots implements a three-reel slot machine. Each spin is a single
// fair RNG draw per reel against the same symbol weight table; the paytable
// awards a multiplier of the bet for three-of-a-kind or two-of-a-kind on
// any line. Designed for the door-games leaderboards, so the house edge is
// modest by .NET-stack convention (~3-5% over a long session).
package slots

import "github.com/nickna/ssh.night.ms/internal/doors"

// Symbol indexes the symbol table. Higher index = rarer + higher payout.
type Symbol int

const (
	Cherry Symbol = iota
	Lemon
	Orange
	Plum
	Bell
	Bar
	Seven
)

var symbolNames = [...]string{"🍒", "🍋", "🍊", "🍇", "🔔", "BAR", "7"}

// Name returns the printable symbol for the slot reel.
func (s Symbol) Name() string {
	if int(s) < 0 || int(s) >= len(symbolNames) {
		return "?"
	}
	return symbolNames[s]
}

// weights map each symbol to its frequency in a virtual 100-stop reel.
// Higher-payout symbols are rarer; the sum is 100 so Intn(100) drives the
// draw cleanly.
var weights = [...]int{
	30, // Cherry — common
	22, // Lemon
	18, // Orange
	14, // Plum
	10, // Bell
	5,  // BAR
	1,  // 7
}

// threeOfAKindMultiplier maps a winning three-of-a-kind symbol to its bet
// multiplier. Two-of-a-kind also pays (smaller). Numbers picked to give a
// jackpot vibe without printing money.
var threeOfAKind = [...]int{
	5,   // Cherry × 3
	10,  // Lemon × 3
	15,  // Orange × 3
	25,  // Plum × 3
	50,  // Bell × 3
	100, // BAR × 3
	250, // 7 × 7 × 7
}

var twoOfAKind = [...]int{
	2, // Cherry × 2 → 2× bet
	2,
	2,
	3,
	3,
	5,
	10,
}

// Spin returns the three reel symbols and the payout multiplier (NOT yet
// multiplied by bet — caller does that and rounds).
type Spin struct {
	Reels      [3]Symbol
	Multiplier int  // 0 when no match
	Winning    bool // true iff Multiplier > 0
}

// Draw runs one spin against the RNG. Pure compute — no I/O.
func Draw(rng doors.CryptoRng) Spin {
	var s Spin
	for i := range s.Reels {
		s.Reels[i] = drawSymbol(rng)
	}
	if s.Reels[0] == s.Reels[1] && s.Reels[1] == s.Reels[2] {
		s.Multiplier = threeOfAKind[s.Reels[0]]
		s.Winning = s.Multiplier > 0
		return s
	}
	if s.Reels[0] == s.Reels[1] || s.Reels[1] == s.Reels[2] || s.Reels[0] == s.Reels[2] {
		// Pick whichever symbol appears twice (matters because two-of-a-kind
		// multipliers vary by symbol).
		var sym Symbol
		switch {
		case s.Reels[0] == s.Reels[1]:
			sym = s.Reels[0]
		case s.Reels[1] == s.Reels[2]:
			sym = s.Reels[1]
		default:
			sym = s.Reels[0]
		}
		s.Multiplier = twoOfAKind[sym]
		s.Winning = s.Multiplier > 0
	}
	return s
}

func drawSymbol(rng doors.CryptoRng) Symbol {
	n := rng.Intn(100)
	cum := 0
	for sym, w := range weights {
		cum += w
		if n < cum {
			return Symbol(sym)
		}
	}
	return Cherry
}

// Payout computes the winnings in credits for a given bet + spin result.
// Multiplier zero -> no payout (loss is just the bet).
func Payout(bet int32, s Spin) int32 {
	if !s.Winning {
		return 0
	}
	return bet * int32(s.Multiplier)
}
