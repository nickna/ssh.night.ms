package roulette

import (
	"testing"

	"github.com/nickna/ssh.night.ms/internal/doors"
)

// TestPocketColors verifies the canonical American wheel coloring: 0/00 are
// green, the 18 documented red numbers are red, and every other 1..36 pocket
// is black.
func TestPocketColors(t *testing.T) {
	redSet := map[Pocket]bool{
		1: true, 3: true, 5: true, 7: true, 9: true, 12: true,
		14: true, 16: true, 18: true, 19: true, 21: true, 23: true,
		25: true, 27: true, 30: true, 32: true, 34: true, 36: true,
	}
	if c := Pocket(0).Color(); c != Green {
		t.Errorf("pocket 0: want Green, got %s", c)
	}
	if c := Pocket00.Color(); c != Green {
		t.Errorf("pocket 00: want Green, got %s", c)
	}
	for n := Pocket(1); n <= 36; n++ {
		want := Black
		if redSet[n] {
			want = Red
		}
		if got := n.Color(); got != want {
			t.Errorf("pocket %s: want %s, got %s", n.Number(), want, got)
		}
	}
}

// TestPocketNumbers checks the face-value strings, including the 00 special
// case. A bug in the lookup table would silently mislabel chips on the felt.
func TestPocketNumbers(t *testing.T) {
	cases := map[Pocket]string{0: "0", 1: "1", 17: "17", 36: "36", Pocket00: "00"}
	for p, want := range cases {
		if got := p.Number(); got != want {
			t.Errorf("pocket %d: want %q, got %q", p, want, got)
		}
	}
}

// TestRibbonOrderCanonical sanity-checks that the ribbon contains all 38
// pockets exactly once. A bug here would mean the spin animation never
// lands on the missing pocket — silently impossible to win straight-up on
// some number.
func TestRibbonOrderCanonical(t *testing.T) {
	if got := len(RibbonOrder); got != PocketCount {
		t.Fatalf("RibbonOrder length: want %d, got %d", PocketCount, got)
	}
	seen := make(map[Pocket]bool, PocketCount)
	for _, p := range RibbonOrder {
		if seen[p] {
			t.Errorf("ribbon: duplicate pocket %s", p.Number())
		}
		seen[p] = true
	}
	for p := Pocket(0); p <= 36; p++ {
		if !seen[p] {
			t.Errorf("ribbon: missing pocket %s", p.Number())
		}
	}
	if !seen[Pocket00] {
		t.Error("ribbon: missing pocket 00")
	}
	for p, idx := range map[Pocket]int{0: 0, Pocket00: 19} {
		if got := RibbonIndex(p); got != idx {
			t.Errorf("RibbonIndex(%s): want %d, got %d", p.Number(), idx, got)
		}
	}
}

// TestEvaluateOutsideBets walks every pocket against each outside-bet type,
// asserting (won, mult) against the published payout table.
func TestEvaluateOutsideBets(t *testing.T) {
	outside := []struct {
		key  BetKey
		mult int32
		pred func(p Pocket) bool
	}{
		{BetKey{Type: BetRed}, 1, Pocket.IsRed},
		{BetKey{Type: BetBlack}, 1, Pocket.IsBlack},
		{BetKey{Type: BetEven}, 1, Pocket.IsEven},
		{BetKey{Type: BetOdd}, 1, Pocket.IsOdd},
		{BetKey{Type: BetLow}, 1, Pocket.IsLow},
		{BetKey{Type: BetHigh}, 1, Pocket.IsHigh},
		{BetKey{Type: BetDozen1}, 2, func(p Pocket) bool { return p.Dozen() == 1 }},
		{BetKey{Type: BetDozen2}, 2, func(p Pocket) bool { return p.Dozen() == 2 }},
		{BetKey{Type: BetDozen3}, 2, func(p Pocket) bool { return p.Dozen() == 3 }},
		{BetKey{Type: BetColumn1}, 2, func(p Pocket) bool { return p.Column() == 1 }},
		{BetKey{Type: BetColumn2}, 2, func(p Pocket) bool { return p.Column() == 2 }},
		{BetKey{Type: BetColumn3}, 2, func(p Pocket) bool { return p.Column() == 3 }},
	}
	allPockets := make([]Pocket, 0, PocketCount)
	for n := Pocket(0); n <= 36; n++ {
		allPockets = append(allPockets, n)
	}
	allPockets = append(allPockets, Pocket00)
	for _, c := range outside {
		for _, p := range allPockets {
			wantWon := c.pred(p)
			gotWon, gotMult := Evaluate(p, c.key)
			if gotWon != wantWon {
				t.Errorf("%s on pocket %s: want won=%v, got %v", c.key, p.Number(), wantWon, gotWon)
				continue
			}
			if wantWon && gotMult != c.mult {
				t.Errorf("%s on pocket %s: want mult=%d, got %d", c.key, p.Number(), c.mult, gotMult)
			}
			if !wantWon && gotMult != 0 {
				t.Errorf("%s on pocket %s: lose path should return mult=0, got %d", c.key, p.Number(), gotMult)
			}
		}
	}
}

// TestZerosLoseAllOutside asserts the house-edge invariant: when 0 or 00
// comes up, every outside wager loses, even Even (a subtle bug once let 0
// satisfy "even"; the test guards against regressing).
func TestZerosLoseAllOutside(t *testing.T) {
	zeros := []Pocket{0, Pocket00}
	outsideTypes := []BetType{
		BetRed, BetBlack, BetEven, BetOdd, BetLow, BetHigh,
		BetDozen1, BetDozen2, BetDozen3,
		BetColumn1, BetColumn2, BetColumn3,
	}
	for _, p := range zeros {
		for _, bt := range outsideTypes {
			won, mult := Evaluate(p, BetKey{Type: bt})
			if won || mult != 0 {
				t.Errorf("pocket %s + %s: zeros must lose outside bets, got (won=%v, mult=%d)", p.Number(), bt, won, mult)
			}
		}
	}
}

// TestStraightUpPaysOnlyMatch confirms straight-up wins exactly when the
// chosen pocket matches the drawn pocket (including 0 and 00).
func TestStraightUpPaysOnlyMatch(t *testing.T) {
	allPockets := make([]Pocket, 0, PocketCount)
	for n := Pocket(0); n <= 36; n++ {
		allPockets = append(allPockets, n)
	}
	allPockets = append(allPockets, Pocket00)
	for _, picked := range allPockets {
		for _, drawn := range allPockets {
			won, mult := Evaluate(drawn, BetKey{Type: BetStraight, Number: picked})
			if drawn == picked {
				if !won || mult != 35 {
					t.Errorf("straight %s drawn %s: want (true,35), got (%v,%d)", picked.Number(), drawn.Number(), won, mult)
				}
			} else {
				if won || mult != 0 {
					t.Errorf("straight %s drawn %s: want loss, got (%v,%d)", picked.Number(), drawn.Number(), won, mult)
				}
			}
		}
	}
}

// TestPayoutAmounts pins down a few hand-checked cases so the (amount × mult)
// math doesn't drift if Evaluate is ever refactored to return different
// multipliers.
func TestPayoutAmounts(t *testing.T) {
	cases := []struct {
		desc      string
		pocket    Pocket
		bet       Bet
		wantPay   int32
		wantGross int32
	}{
		{"red bet on red pocket", Pocket(7), Bet{BetKey{Type: BetRed}, 5}, 5, 10},
		{"red bet on black pocket", Pocket(2), Bet{BetKey{Type: BetRed}, 5}, 0, 0},
		{"red bet on zero", Pocket(0), Bet{BetKey{Type: BetRed}, 5}, 0, 0},
		{"dozen1 bet on pocket 7", Pocket(7), Bet{BetKey{Type: BetDozen1}, 10}, 20, 30},
		{"dozen1 bet on pocket 13", Pocket(13), Bet{BetKey{Type: BetDozen1}, 10}, 0, 0},
		{"straight 17 on 17", Pocket(17), Bet{BetKey{Type: BetStraight, Number: 17}, 1}, 35, 36},
		{"straight 17 on 18", Pocket(18), Bet{BetKey{Type: BetStraight, Number: 17}, 1}, 0, 0},
		{"straight 00 on 00", Pocket00, Bet{BetKey{Type: BetStraight, Number: Pocket00}, 2}, 70, 72},
	}
	for _, c := range cases {
		if got := Payout(c.pocket, c.bet); got != c.wantPay {
			t.Errorf("%s: Payout want %d, got %d", c.desc, c.wantPay, got)
		}
		if got := GrossReturn(c.pocket, c.bet); got != c.wantGross {
			t.Errorf("%s: GrossReturn want %d, got %d", c.desc, c.wantGross, got)
		}
	}
}

// TestBetKeyString ensures each key serialises stably; the coordinator uses
// these strings as map keys in PhaseMsg.Aggregate so a collision would
// double-count chips across bet types.
func TestBetKeyString(t *testing.T) {
	cases := map[BetKey]string{
		{Type: BetRed}:                          "red",
		{Type: BetDozen2}:                       "2nd12",
		{Type: BetStraight, Number: 17}:         "straight:17",
		{Type: BetStraight, Number: Pocket00}:   "straight:00",
		{Type: BetStraight, Number: Pocket(0)}:  "straight:0",
	}
	seen := make(map[string]BetKey, len(cases))
	for k, want := range cases {
		got := k.String()
		if got != want {
			t.Errorf("BetKey(%v): want %q, got %q", k, want, got)
		}
		if other, dup := seen[got]; dup && other != k {
			t.Errorf("BetKey collision: %q used by both %v and %v", got, other, k)
		}
		seen[got] = k
	}
}

// TestDrawInRange runs many draws and verifies every result is a valid
// pocket index. Doesn't assert distribution — that's crypto/rand's contract.
func TestDrawInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		p := Draw(doors.CryptoRng{})
		if int(p) >= PocketCount {
			t.Fatalf("Draw returned out-of-range pocket: %d", p)
		}
	}
}
