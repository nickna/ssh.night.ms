package roulette

import "fmt"

// BetType enumerates every wager type the BBS roulette table accepts.
// Outside bets (Red/Black/Even/Odd/Low/High, Dozens, Columns) plus the
// single-number "straight-up". Inside combinations (splits, streets,
// corners, lines) are intentionally out of scope for the BBS UI.
type BetType uint8

const (
	BetStraight BetType = iota // single number; requires BetKey.Number
	BetRed
	BetBlack
	BetEven
	BetOdd
	BetLow  // pockets 1..18
	BetHigh // pockets 19..36
	BetDozen1
	BetDozen2
	BetDozen3
	BetColumn1
	BetColumn2
	BetColumn3
)

func (t BetType) String() string {
	switch t {
	case BetStraight:
		return "straight"
	case BetRed:
		return "red"
	case BetBlack:
		return "black"
	case BetEven:
		return "even"
	case BetOdd:
		return "odd"
	case BetLow:
		return "low"
	case BetHigh:
		return "high"
	case BetDozen1:
		return "1st12"
	case BetDozen2:
		return "2nd12"
	case BetDozen3:
		return "3rd12"
	case BetColumn1:
		return "col1"
	case BetColumn2:
		return "col2"
	case BetColumn3:
		return "col3"
	}
	return "?"
}

// BetKey is the hashable identity of a wager position. Chips placed on the
// same key stack together (used by the coordinator's aggregate map so other
// players see "12 chips on RED" rather than 12 individual rows).
type BetKey struct {
	Type   BetType
	Number Pocket // meaningful only for BetStraight
}

// String returns a stable serialisation suitable as a map key in JSON
// payloads. Straight-up bets append the pocket face value so 17 and 18
// don't collide.
func (k BetKey) String() string {
	if k.Type == BetStraight {
		return fmt.Sprintf("straight:%s", k.Number.Number())
	}
	return k.Type.String()
}

// Bet is one wager record — a key plus the amount in credits.
type Bet struct {
	Key    BetKey
	Amount int32
}

// Evaluate returns whether the wager wins on the given pocket and, if so,
// the *winnings* multiplier (so the gross payout is amount × (1 + mult)).
// Multipliers:
//
//	BetStraight                                  → 35:1
//	BetDozen{1,2,3}, BetColumn{1,2,3}            → 2:1
//	BetRed, BetBlack, BetEven, BetOdd,
//	BetLow, BetHigh                              → 1:1
//
// All outside bets lose on 0 and 00 (the house edge baked into American
// roulette). A straight-up bet on 0 or 00 still wins at 35:1 when the
// matching pocket comes up.
func Evaluate(p Pocket, k BetKey) (won bool, mult int32) {
	switch k.Type {
	case BetStraight:
		if p == k.Number {
			return true, 35
		}
		return false, 0
	case BetRed:
		if p.IsRed() {
			return true, 1
		}
	case BetBlack:
		if p.IsBlack() {
			return true, 1
		}
	case BetEven:
		if p.IsEven() {
			return true, 1
		}
	case BetOdd:
		if p.IsOdd() {
			return true, 1
		}
	case BetLow:
		if p.IsLow() {
			return true, 1
		}
	case BetHigh:
		if p.IsHigh() {
			return true, 1
		}
	case BetDozen1:
		if p.Dozen() == 1 {
			return true, 2
		}
	case BetDozen2:
		if p.Dozen() == 2 {
			return true, 2
		}
	case BetDozen3:
		if p.Dozen() == 3 {
			return true, 2
		}
	case BetColumn1:
		if p.Column() == 1 {
			return true, 2
		}
	case BetColumn2:
		if p.Column() == 2 {
			return true, 2
		}
	case BetColumn3:
		if p.Column() == 3 {
			return true, 2
		}
	}
	return false, 0
}

// Payout returns the *winnings* in credits for a settled bet (0 on a loss).
// The original stake was already debited at place-time; coordinator credits
// Amount + Payout to the winner so the house keeps the stake on a loss.
func Payout(p Pocket, b Bet) int32 {
	won, mult := Evaluate(p, b.Key)
	if !won {
		return 0
	}
	return b.Amount * mult
}

// GrossReturn is the total amount returned to the wallet on a win: stake plus
// winnings. Coordinator uses this to issue a single Credit call per winning
// bet — clearer than two Credits of stake + payout. On a loss returns 0.
func GrossReturn(p Pocket, b Bet) int32 {
	won, mult := Evaluate(p, b.Key)
	if !won {
		return 0
	}
	return b.Amount * (1 + mult)
}
