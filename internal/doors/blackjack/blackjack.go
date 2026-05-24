// Package blackjack implements a single-deck blackjack game: dealer hits
// soft 17, blackjack pays 3:2, double on first action only, no split (v1).
// The same Game value is read by the screen each frame — Update returns
// nothing; the screen polls state.
package blackjack

import (
	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
)

// Outcome enumerates the end-of-hand result. Player win includes blackjack;
// Push when both totals match. Surface separately so the screen can show a
// different payout multiplier.
type Outcome int

const (
	InProgress Outcome = iota
	PlayerBust
	DealerBust
	PlayerWin
	PlayerBlackjack
	DealerWin
	Push
)

func (o Outcome) String() string {
	switch o {
	case PlayerBust:
		return "Bust"
	case DealerBust:
		return "Dealer busts"
	case PlayerWin:
		return "Player wins"
	case PlayerBlackjack:
		return "Blackjack!"
	case DealerWin:
		return "Dealer wins"
	case Push:
		return "Push"
	}
	return ""
}

// Game holds one hand's state. Dealer's hole card is at HoleIndex while the
// player decides; the screen looks at HoleHidden() to know whether to mask
// it.
type Game struct {
	deck     []cards.Card
	player   []cards.Card
	dealer   []cards.Card
	doubled  bool
	finished bool
	hole     bool // true while dealer's second card is hidden
	outcome  Outcome
}

// NewGame builds + shuffles + deals the opening two-cards-each layout, with
// the dealer's second card face-down.
func NewGame(rng doors.CryptoRng) *Game {
	deck := cards.NewDeck()
	cards.Shuffle(deck, rng)
	g := &Game{deck: deck, hole: true}
	g.player = append(g.player, g.draw(), g.draw())
	g.dealer = append(g.dealer, g.draw(), g.draw())
	// Immediate blackjack handling: peek for player BJ; if also dealer BJ → push.
	if HandValue(g.player) == 21 {
		g.hole = false
		if HandValue(g.dealer) == 21 {
			g.outcome = Push
		} else {
			g.outcome = PlayerBlackjack
		}
		g.finished = true
	}
	return g
}

func (g *Game) draw() cards.Card {
	c := g.deck[0]
	g.deck = g.deck[1:]
	return c
}

// Player + Dealer return read-only snapshots so the screen doesn't mutate
// game state by accident.
func (g *Game) Player() []cards.Card { return append([]cards.Card(nil), g.player...) }
func (g *Game) Dealer() []cards.Card { return append([]cards.Card(nil), g.dealer...) }

// HoleHidden reports whether the dealer's second card should be masked in
// the view layer.
func (g *Game) HoleHidden() bool { return g.hole }

// Finished + Outcome are how the screen knows the hand is over.
func (g *Game) Finished() bool   { return g.finished }
func (g *Game) Outcome() Outcome { return g.outcome }
func (g *Game) Doubled() bool    { return g.doubled }

// Hit adds a card to the player's hand. Stand/Double end the player's turn.
// All three settle to dealer play + outcome when the player can no longer
// act.
func (g *Game) Hit() {
	if g.finished {
		return
	}
	g.player = append(g.player, g.draw())
	if HandValue(g.player) > 21 {
		g.hole = false
		g.outcome = PlayerBust
		g.finished = true
	}
}

// Stand passes turn to the dealer.
func (g *Game) Stand() {
	if g.finished {
		return
	}
	g.dealerPlay()
}

// Double takes exactly one more card and stands. Bet doubling happens at
// the caller (wallet); we just flag it so the payout multiplier is correct.
func (g *Game) Double() {
	if g.finished || len(g.player) != 2 {
		return
	}
	g.doubled = true
	g.player = append(g.player, g.draw())
	if HandValue(g.player) > 21 {
		g.hole = false
		g.outcome = PlayerBust
		g.finished = true
		return
	}
	g.dealerPlay()
}

// CanDouble reports whether double is allowed (first action only).
func (g *Game) CanDouble() bool {
	return !g.finished && len(g.player) == 2
}

func (g *Game) dealerPlay() {
	g.hole = false
	for {
		v, soft := value(g.dealer)
		if v < 17 || (v == 17 && soft) {
			g.dealer = append(g.dealer, g.draw())
			continue
		}
		break
	}
	dv := HandValue(g.dealer)
	pv := HandValue(g.player)
	switch {
	case dv > 21:
		g.outcome = DealerBust
	case pv > dv:
		g.outcome = PlayerWin
	case pv < dv:
		g.outcome = DealerWin
	default:
		g.outcome = Push
	}
	g.finished = true
}

// HandValue returns the best total ≤ 21 for a hand. Aces count as 11 until
// that would bust, in which case they drop to 1 one at a time. Exported so
// the screen can render it.
func HandValue(h []cards.Card) int {
	v, _ := value(h)
	return v
}

// value returns (total, soft). soft is true when at least one ace is still
// counted as 11. Dealer logic needs soft to decide on a soft 17 hit.
func value(h []cards.Card) (int, bool) {
	total := 0
	aces := 0
	for _, c := range h {
		switch c.Rank {
		case cards.Ace:
			total += 11
			aces++
		case cards.Jack, cards.Queen, cards.King:
			total += 10
		default:
			total += int(c.Rank)
		}
	}
	for total > 21 && aces > 0 {
		total -= 10
		aces--
	}
	return total, aces > 0
}

// Payout converts outcome + bet into a wallet credit (gross payout, not net).
// Bet was already debited at game start; double adds a second bet debit.
//   - Blackjack: bet × 5/2 (returns 1× bet stake + 1.5× win)
//   - Win:       bet × 2 (stake + 1×)
//   - Push:      bet × 1 (stake returned)
//   - Bust/Loss: 0
//   - Doubled:   bet doubled for win/loss; original-bet stake on push
func Payout(bet int32, doubled bool, o Outcome) int32 {
	stake := bet
	if doubled {
		stake = bet * 2
	}
	switch o {
	case PlayerBlackjack:
		// BJ never doubles in v1, so stake == bet.
		return bet*2 + bet/2 // 2× stake + 0.5× stake = 2.5×
	case PlayerWin, DealerBust:
		return stake * 2
	case Push:
		return stake
	}
	return 0
}
