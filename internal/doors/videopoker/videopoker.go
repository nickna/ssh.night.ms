// Package videopoker implements 9/6 Jacks or Better — the gold-standard
// payback variant. Deal five cards; the player holds any subset; remaining
// cards are replaced from the same deck; the final hand is evaluated against
// the paytable. House edge < 1% with perfect strategy, which we don't
// enforce on the player.
package videopoker

import (
	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
)

// PayTable9_6 is the canonical 9/6 Jacks or Better schedule, indexed by
// HandRank → multiplier of the bet. 9 for full house, 6 for flush — the
// "9/6" designation. Values are flat per-credit; bet × multiplier = payout.
var PayTable9_6 = map[cards.HandRank]int{
	cards.JacksOrBetter: 1,
	cards.TwoPair:       2,
	cards.ThreeOfAKind:  3,
	cards.Straight:      4,
	cards.Flush:         6,
	cards.FullHouse:     9,
	cards.FourOfAKind:   25,
	cards.StraightFlush: 50,
	cards.RoyalFlush:    800,
}

// PayTableRow is a presentation row for the paytable view.
type PayTableRow struct {
	Name       string
	Multiplier int
}

// Schedule returns paytable rows in descending payout order, ready to render.
func Schedule() []PayTableRow {
	order := []cards.HandRank{
		cards.RoyalFlush, cards.StraightFlush, cards.FourOfAKind, cards.FullHouse,
		cards.Flush, cards.Straight, cards.ThreeOfAKind, cards.TwoPair,
		cards.JacksOrBetter,
	}
	out := make([]PayTableRow, 0, len(order))
	for _, hr := range order {
		out = append(out, PayTableRow{Name: hr.String(), Multiplier: PayTable9_6[hr]})
	}
	return out
}

// Game tracks one hand from deal through draw. The same Game value is read by
// the screen each frame — it doesn't store any view state, just game state.
type Game struct {
	deck  []cards.Card // remaining cards after deal (mutates on Draw)
	hand  [5]cards.Card
	held  [5]bool
	dealt bool
	drawn bool

	finalRank cards.HandRank
	payout    int32
}

// NewGame builds a fresh game and shuffles. The caller still has to call
// Deal — keeps the constructor cheap and the side effect explicit.
func NewGame(rng doors.CryptoRng) *Game {
	deck := cards.NewDeck()
	cards.Shuffle(deck, rng)
	return &Game{deck: deck}
}

// Deal lays out the initial 5 cards. Idempotent within a Game — calling
// twice has no effect.
func (g *Game) Deal() {
	if g.dealt {
		return
	}
	for i := range g.hand {
		g.hand[i] = g.deck[0]
		g.deck = g.deck[1:]
	}
	g.dealt = true
}

// Hand returns the current 5-card hand.
func (g *Game) Hand() [5]cards.Card { return g.hand }

// Held reports per-slot hold state.
func (g *Game) Held() [5]bool { return g.held }

// ToggleHold flips the hold state of slot i (0-4). No-op outside range or
// after draw.
func (g *Game) ToggleHold(i int) {
	if i < 0 || i >= 5 || g.drawn {
		return
	}
	g.held[i] = !g.held[i]
}

// Draw replaces non-held cards from the deck, evaluates, and returns the
// payout multiplier (0 for no win). Subsequent calls no-op.
func (g *Game) Draw(bet int32) (rank cards.HandRank, payout int32) {
	if g.drawn {
		return g.finalRank, g.payout
	}
	for i := range g.hand {
		if !g.held[i] {
			g.hand[i] = g.deck[0]
			g.deck = g.deck[1:]
		}
	}
	g.finalRank = cards.Evaluate(g.hand[:])
	if mult, ok := PayTable9_6[g.finalRank]; ok {
		g.payout = bet * int32(mult)
	}
	g.drawn = true
	return g.finalRank, g.payout
}

// State exposes whether deal/draw have happened — used by the screen to pick
// what controls to render.
func (g *Game) State() (dealt, drawn bool) { return g.dealt, g.drawn }

// Result is the final hand+payout once Drawn() is true; zero before.
func (g *Game) Result() (cards.HandRank, int32) { return g.finalRank, g.payout }
