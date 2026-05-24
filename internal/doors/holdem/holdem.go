// Package holdem implements heads-up Texas Hold'em vs one CPU bot. The
// multiplayer table actor model lives in the sibling multiplayer/ package.
//
// Lifecycle per hand:
//   1. Post blinds (CPU=SB, player=BB or vice-versa, alternating).
//   2. Deal hole cards.
//   3. Preflop betting round (button acts first preflop in HU).
//   4. Flop, turn, river — each followed by a betting round.
//   5. Showdown or fold; pot awarded; advance button.
package holdem

import (
	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
)

// Street enumerates the four betting rounds + showdown end-state.
type Street int

const (
	Preflop Street = iota
	Flop
	Turn
	River
	Showdown
	Ended // pot already awarded
)

func (s Street) String() string {
	switch s {
	case Preflop:
		return "Preflop"
	case Flop:
		return "Flop"
	case Turn:
		return "Turn"
	case River:
		return "River"
	case Showdown:
		return "Showdown"
	case Ended:
		return "Ended"
	}
	return ""
}

// Seat 0 is the player; Seat 1 is the bot. Button rotates each hand.
const (
	SeatPlayer = 0
	SeatBot    = 1
)

// Action is one player's choice during a betting round.
type Action int

const (
	ActFold Action = iota
	ActCheckCall
	ActRaise // raises by the minimum legal amount in v1
	ActAllIn
)

// Game holds one hand's full state. The screen owns one *Game and replaces it
// at the start of each hand. Doesn't itself debit the player's wallet — the
// screen does that around hand start.
type Game struct {
	deck   []cards.Card
	hole   [2][2]cards.Card // hole[seat] = 2 cards
	board  []cards.Card
	stacks [2]int32 // chips behind, per seat
	bets   [2]int32 // committed this street, per seat
	pot    int32
	button int // seat that has the button (acts last postflop, first preflop in HU)
	toAct  int // seat whose turn it is
	street Street
	folded [2]bool

	smallBlind int32
	bigBlind   int32
	rng        doors.CryptoRng

	winnerSeat int   // -1 = no winner yet
	winRank    cards.HandRank
	winBoard   bool // true at showdown so screen reveals bot's cards
}

// New constructs a fresh game with stacks + blinds + a starting button. Call
// StartHand() after this to deal.
func New(rng doors.CryptoRng, startingStack int32, sb, bb int32, button int) *Game {
	return &Game{
		stacks:     [2]int32{startingStack, startingStack},
		smallBlind: sb,
		bigBlind:   bb,
		button:     button,
		winnerSeat: -1,
		rng:        rng,
	}
}

// StartHand resets street state, posts blinds, deals hole cards, sets first
// actor (in HU, button = SB = first to act preflop).
func (g *Game) StartHand() {
	g.deck = cards.NewDeck()
	cards.Shuffle(g.deck, g.rng)
	g.board = nil
	g.bets = [2]int32{}
	g.folded = [2]bool{}
	g.pot = 0
	g.winnerSeat = -1
	g.winRank = 0
	g.winBoard = false
	g.street = Preflop

	sb := g.button
	bb := 1 - g.button
	g.postBet(sb, g.smallBlind)
	g.postBet(bb, g.bigBlind)

	for s := 0; s < 2; s++ {
		g.hole[s][0] = g.draw()
		g.hole[s][1] = g.draw()
	}
	g.toAct = sb // HU preflop: SB (button) acts first
}

func (g *Game) draw() cards.Card {
	c := g.deck[0]
	g.deck = g.deck[1:]
	return c
}

// postBet moves chips from stack to bet, capped by stack. Used for blinds
// and the chip-movement side of every betting action.
func (g *Game) postBet(seat int, amount int32) {
	if amount > g.stacks[seat] {
		amount = g.stacks[seat]
	}
	g.stacks[seat] -= amount
	g.bets[seat] += amount
}

// PlayerHole + BoardCards + Stacks + Bets + Pot + Street + ToAct expose state
// for the screen. Bot's hole is only revealed at showdown.
func (g *Game) PlayerHole() [2]cards.Card { return g.hole[SeatPlayer] }
func (g *Game) BotHole() ([2]cards.Card, bool) {
	if g.street == Showdown || g.street == Ended {
		return g.hole[SeatBot], g.winBoard
	}
	return [2]cards.Card{}, false
}
func (g *Game) Board() []cards.Card      { return append([]cards.Card(nil), g.board...) }
func (g *Game) Stacks() [2]int32         { return g.stacks }
func (g *Game) Bets() [2]int32           { return g.bets }
func (g *Game) Pot() int32               { return g.pot + g.bets[0] + g.bets[1] }
func (g *Game) Street() Street           { return g.street }
func (g *Game) ToAct() int               { return g.toAct }
func (g *Game) WinnerSeat() int          { return g.winnerSeat }
func (g *Game) WinRank() cards.HandRank  { return g.winRank }
func (g *Game) BigBlind() int32          { return g.bigBlind }

// ToCall returns the amount the to-act seat must put in to call.
func (g *Game) ToCall() int32 {
	return g.bets[1-g.toAct] - g.bets[g.toAct]
}

// HandActive reports whether the hand is still mid-flight (i.e. waiting for
// player or bot action). When false the screen prompts a new hand.
func (g *Game) HandActive() bool {
	return g.street != Ended
}

// PlayerAction processes one action from the player seat. Returns true when
// the action ended the hand (fold or all-in showdown).
func (g *Game) PlayerAction(a Action) bool {
	if g.toAct != SeatPlayer || g.street == Ended {
		return false
	}
	g.applyAction(SeatPlayer, a)
	return g.afterAction()
}

// StepBot runs one bot action if it's the bot's turn. Returns whether the
// hand ended. Screen calls this in a loop after each player action so the
// bot's reply lands immediately.
func (g *Game) StepBot() bool {
	if g.toAct != SeatBot || g.street == Ended {
		return false
	}
	a := g.botPolicy()
	g.applyAction(SeatBot, a)
	return g.afterAction()
}

// applyAction mutates state for one player's choice. The .NET stack's full
// raise sizing isn't implemented — v1 raises by exactly one big blind to
// keep the play simple and predictable for the CPU.
func (g *Game) applyAction(seat int, a Action) {
	toCall := g.bets[1-seat] - g.bets[seat]
	switch a {
	case ActFold:
		g.folded[seat] = true
	case ActCheckCall:
		if toCall > 0 {
			g.postBet(seat, toCall)
		}
	case ActRaise:
		// Call any outstanding bet, then add a raise of 1 BB (v1).
		if toCall > 0 {
			g.postBet(seat, toCall)
		}
		g.postBet(seat, g.bigBlind)
	case ActAllIn:
		g.postBet(seat, g.stacks[seat])
	}
}

// afterAction advances state: fold ends the hand; otherwise either yield to
// the other seat or advance to the next street if both are even. Returns
// true when the hand has fully ended.
func (g *Game) afterAction() bool {
	if g.folded[SeatPlayer] || g.folded[SeatBot] {
		g.endByFold()
		return true
	}
	// If bets aren't even, the other seat needs to act.
	if g.bets[0] != g.bets[1] {
		g.toAct = 1 - g.toAct
		return false
	}
	// Postflop: button acts first... but in v1 we only need a "did the BB
	// get a chance to act" guard preflop. Simplification: if both seats have
	// matched bets, the street is closed.
	return g.closeStreet()
}

// closeStreet moves chips from bets[] → pot, deals the next community card,
// and resets the actor. Returns true when the hand fully ends (after river).
func (g *Game) closeStreet() bool {
	g.pot += g.bets[0] + g.bets[1]
	g.bets = [2]int32{}
	switch g.street {
	case Preflop:
		g.burn()
		g.board = append(g.board, g.draw(), g.draw(), g.draw())
		g.street = Flop
	case Flop:
		g.burn()
		g.board = append(g.board, g.draw())
		g.street = Turn
	case Turn:
		g.burn()
		g.board = append(g.board, g.draw())
		g.street = River
	case River:
		g.street = Showdown
		g.resolveShowdown()
		return true
	}
	// Postflop: out-of-position (non-button) acts first. In HU, button = SB.
	g.toAct = 1 - g.button
	return false
}

func (g *Game) burn() {
	if len(g.deck) > 0 {
		g.deck = g.deck[1:]
	}
}

func (g *Game) endByFold() {
	winner := SeatPlayer
	if g.folded[SeatPlayer] {
		winner = SeatBot
	}
	// Award entire pot to winner (committed + already collected).
	g.stacks[winner] += g.pot + g.bets[0] + g.bets[1]
	g.pot = 0
	g.bets = [2]int32{}
	g.winnerSeat = winner
	g.street = Ended
}

func (g *Game) resolveShowdown() {
	p := append([]cards.Card{g.hole[SeatPlayer][0], g.hole[SeatPlayer][1]}, g.board...)
	b := append([]cards.Card{g.hole[SeatBot][0], g.hole[SeatBot][1]}, g.board...)
	pr, ptb := cards.EvaluateBest(p)
	br, btb := cards.EvaluateBest(b)
	winner := -1
	switch {
	case pr > br || (pr == br && ptb > btb):
		winner = SeatPlayer
	case br > pr || (br == pr && btb > ptb):
		winner = SeatBot
	default:
		// True chop — split the pot. v1 just floor-divides; odd chip stays in
		// pot, awarded to player by convention (cheaper than tracking last-
		// raiser for now).
		g.stacks[0] += g.pot / 2
		g.stacks[1] += g.pot / 2
		if g.pot%2 == 1 {
			g.stacks[0]++
		}
		g.pot = 0
		g.winnerSeat = -1
		g.winRank = pr
		g.winBoard = true
		g.street = Ended
		return
	}
	g.stacks[winner] += g.pot
	g.pot = 0
	g.winnerSeat = winner
	if winner == SeatPlayer {
		g.winRank = pr
	} else {
		g.winRank = br
	}
	g.winBoard = true
	g.street = Ended
}

// botPolicy is a simple equity-tinted strategy: fold trash to a raise, call
// most of the time, raise made hands or strong draws. Not strong, but not
// trivially exploitable in HU.
func (g *Game) botPolicy() Action {
	rank, _ := cards.EvaluateBest(append([]cards.Card{g.hole[SeatBot][0], g.hole[SeatBot][1]}, g.board...))
	toCall := g.ToCall()
	if g.street == Preflop {
		// Preflop: action on a fresh deal — fold ~25% of trash to a raise,
		// open-raise pairs/AK randomly, otherwise call.
		strong := isPreflopStrong(g.hole[SeatBot])
		switch {
		case toCall > g.bigBlind && !strong:
			if g.rng.Intn(4) == 0 {
				return ActFold
			}
			return ActCheckCall
		case strong && g.rng.Intn(3) == 0:
			return ActRaise
		default:
			return ActCheckCall
		}
	}
	// Postflop: raise made hands, call otherwise, fold air against pressure.
	if rank >= cards.TwoPair {
		if g.rng.Intn(3) == 0 {
			return ActRaise
		}
		return ActCheckCall
	}
	if rank == cards.HighCard && toCall > g.bigBlind*2 {
		return ActFold
	}
	return ActCheckCall
}

func isPreflopStrong(h [2]cards.Card) bool {
	// Pair, AK, AQ, AJ, KQ.
	if h[0].Rank == h[1].Rank {
		return true
	}
	high := h[0].Rank
	low := h[1].Rank
	if low > high {
		high, low = low, high
	}
	if high == cards.Ace && low >= cards.Jack {
		return true
	}
	if high == cards.King && low == cards.Queen {
		return true
	}
	return false
}

// Folded reports whether a seat has folded (used for end-of-hand summary).
func (g *Game) Folded(seat int) bool { return g.folded[seat] }

// AdvanceButton rotates the button to the other seat. Screen calls this
// when starting the next hand.
func (g *Game) AdvanceButton() { g.button = 1 - g.button }

// Button returns the seat with the button this hand.
func (g *Game) Button() int { return g.button }
