// Package multiplayer is the actor-model multi-seat Hold'em engine.
//
// Threading model: each Table owns one goroutine that reads from a single
// input channel and serializes every state mutation. Subscribers connect via
// Subscribe() and receive State snapshots on a buffered output channel.
//
// Persistence is intentionally minimal — settled hands write to game_rounds
// like the single-player flows, but live table state lives in-memory until
// the coordinator shuts down (cash-out-all on graceful stop).
package multiplayer

import (
	"sort"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	"github.com/nickna/ssh.night.ms/internal/doors/holdem"
)

// Street, Action mirror holdem.Street + holdem.Action. Re-exported so the
// multiplayer caller doesn't have to import two packages for the type
// catalog.
type Street = holdem.Street
type Action = holdem.Action

const (
	Preflop  = holdem.Preflop
	Flop     = holdem.Flop
	Turn     = holdem.Turn
	River    = holdem.River
	Showdown = holdem.Showdown
	Ended    = holdem.Ended

	ActFold      = holdem.ActFold
	ActCheckCall = holdem.ActCheckCall
	ActRaise     = holdem.ActRaise
	ActAllIn     = holdem.ActAllIn
)

// Seat is one player's slot at the table. UserID = 0 means the seat is
// empty. ChipsHand is the live stack mid-hand; ChipsBet is committed to the
// current street; HoleVisibleTo is the user IDs that get to see the hole
// cards (always the seat's own user, plus everyone at showdown).
type Seat struct {
	UserID    int64
	Handle    string
	ChipsHand int32
	Bet       int32 // committed THIS street
	Committed int32 // committed TOTAL THIS HAND — drives side-pot construction
	Hole      [2]cards.Card
	Folded    bool
	AllIn     bool
}

// Game is the N-seat Hold'em engine. Generalized from the heads-up version
// in the parent package — same payout math, same Street/Action enums.
type Game struct {
	deck   []cards.Card
	seats  []Seat
	board  []cards.Card

	button     int // seat index that has the button this hand
	toAct      int // next seat to act; -1 between hands
	pot        int32
	street     Street
	smallBlind int32
	bigBlind   int32
	rng        doors.CryptoRng

	// lastRaiserIdx marks who put in the most recent raise (or open).
	// Action ends when every non-folded seat after them has either matched
	// the bet or folded.
	lastRaiserIdx int

	winner int // winning seat index after hand resolved; -1 when split
	winRank cards.HandRank

	// lastPayouts is the per-seat chips awarded this hand, indexed by seat
	// number. Populated in endByFold + resolveShowdown; reset on StartHand.
	// The coordinator reads this to build per-human settlement rows.
	lastPayouts []int32

	// handNumber increments at the top of every StartHand. The coordinator
	// uses it as the hand_no column in multiplayer_hands; persisted through
	// the snapshot so it survives restart and the (table_id, hand_no)
	// unique index doesn't collide.
	handNumber int64
}

// NewGame builds an engine sized for cap seats. Seats start empty.
func NewGame(rng doors.CryptoRng, cap int, sb, bb int32) *Game {
	if cap < 2 {
		cap = 2
	}
	if cap > 9 {
		cap = 9
	}
	g := &Game{
		seats:       make([]Seat, cap),
		lastPayouts: make([]int32, cap),
		smallBlind:  sb,
		bigBlind:    bb,
		rng:         rng,
		street:      Ended,
		winner:      -1,
	}
	return g
}

// SeatCount returns the configured cap, regardless of how many seats are
// currently occupied.
func (g *Game) SeatCount() int { return len(g.seats) }

// Seats returns a copy of the seat array so the caller can read state
// without taking a reference to engine internals. Length equals cap.
func (g *Game) Seats() []Seat {
	out := make([]Seat, len(g.seats))
	copy(out, g.seats)
	return out
}

// OccupiedSeats returns indexes of seats currently filled (UserID != 0).
func (g *Game) OccupiedSeats() []int {
	out := make([]int, 0, len(g.seats))
	for i, s := range g.seats {
		if s.UserID != 0 {
			out = append(out, i)
		}
	}
	return out
}

// Board, Pot, Street, ToAct, Button, BigBlind expose state.
func (g *Game) Board() []cards.Card      { return append([]cards.Card(nil), g.board...) }
func (g *Game) Pot() int32 {
	total := g.pot
	for _, s := range g.seats {
		total += s.Bet
	}
	return total
}
func (g *Game) Street() Street      { return g.street }
func (g *Game) ToAct() int          { return g.toAct }
func (g *Game) Button() int         { return g.button }
func (g *Game) BigBlind() int32     { return g.bigBlind }
func (g *Game) Winner() int         { return g.winner }
func (g *Game) WinRank() cards.HandRank { return g.winRank }

// Payouts returns per-seat chips awarded by the most recently settled hand,
// indexed by seat number. Zero for seats that won nothing (or weren't at
// the table). The slice is the live backing array — copy before mutation.
func (g *Game) Payouts() []int32 { return g.lastPayouts }

// HandNumber returns the running counter incremented at the top of every
// StartHand. The coordinator passes this as the hand_no column in the
// multiplayer_hands row.
func (g *Game) HandNumber() int64 { return g.handNumber }

// Sit places a player at seat index with chipsStarting chips. Returns false
// if the seat is out of range, occupied, or chips invalid.
func (g *Game) Sit(seatIdx int, userID int64, handle string, chips int32) bool {
	if seatIdx < 0 || seatIdx >= len(g.seats) {
		return false
	}
	if g.seats[seatIdx].UserID != 0 {
		return false
	}
	if chips <= 0 {
		return false
	}
	g.seats[seatIdx] = Seat{UserID: userID, Handle: handle, ChipsHand: chips}
	return true
}

// Stand removes the player at seat index. Returns the chips they leave with
// (their live ChipsHand), or 0 if the seat was empty / not them. Mid-hand
// stands fold the seat first; chips committed to the pot stay there.
func (g *Game) Stand(seatIdx int, userID int64) int32 {
	if seatIdx < 0 || seatIdx >= len(g.seats) {
		return 0
	}
	s := g.seats[seatIdx]
	if s.UserID != userID {
		return 0
	}
	// Mid-hand stand: forfeit any committed chips, take the remaining stack.
	chips := s.ChipsHand
	g.seats[seatIdx] = Seat{}
	if g.street != Ended {
		// If the standing player owed action, advance.
		if g.toAct == seatIdx {
			g.advanceAction()
		}
		// One-player-left fast-path.
		if g.activeCount() <= 1 {
			g.endByFold()
		}
	}
	return chips
}

// activeCount = seats with chips behind OR a non-zero current bet, not folded.
func (g *Game) activeCount() int {
	n := 0
	for _, s := range g.seats {
		if s.UserID != 0 && !s.Folded {
			n++
		}
	}
	return n
}

// StartHand begins the next hand: rotate button, post blinds, deal hole
// cards, set toAct. Requires ≥2 occupied non-folded seats. Returns false
// if the table can't deal.
func (g *Game) StartHand() bool {
	occ := g.OccupiedSeats()
	if len(occ) < 2 {
		return false
	}
	for i := range g.seats {
		g.seats[i].Folded = false
		g.seats[i].AllIn = false
		g.seats[i].Bet = 0
		g.seats[i].Committed = 0
		g.seats[i].Hole = [2]cards.Card{}
	}
	for i := range g.lastPayouts {
		g.lastPayouts[i] = 0
	}
	g.handNumber++
	g.board = nil
	g.pot = 0
	g.winner = -1
	g.winRank = 0

	// Advance the button to the next occupied seat after the current one.
	if g.button < 0 {
		g.button = occ[0]
	} else {
		g.button = g.nextOccupied(g.button)
	}

	// Shuffle a fresh deck.
	g.deck = cards.NewDeck()
	cards.Shuffle(g.deck, g.rng)

	sbSeat := g.nextOccupied(g.button)
	bbSeat := g.nextOccupied(sbSeat)
	if len(occ) == 2 {
		// HU: button = SB, the other player = BB.
		sbSeat = g.button
		bbSeat = g.nextOccupied(g.button)
	}
	g.postBlind(sbSeat, g.smallBlind)
	g.postBlind(bbSeat, g.bigBlind)
	g.lastRaiserIdx = bbSeat

	// Deal two cards to every occupied seat in clockwise order from SB.
	cursor := sbSeat
	for round := 0; round < 2; round++ {
		for i := 0; i < len(occ); i++ {
			g.seats[cursor].Hole[round] = g.draw()
			cursor = g.nextOccupied(cursor)
		}
	}
	g.street = Preflop
	if len(occ) == 2 {
		// HU preflop: SB (button) acts first.
		g.toAct = sbSeat
	} else {
		// 3+ players: action starts left of BB.
		g.toAct = g.nextOccupied(bbSeat)
	}
	return true
}

// postBlind posts a forced bet (up to ChipsHand if shorter).
func (g *Game) postBlind(seatIdx int, amount int32) {
	if amount > g.seats[seatIdx].ChipsHand {
		amount = g.seats[seatIdx].ChipsHand
	}
	g.seats[seatIdx].ChipsHand -= amount
	g.seats[seatIdx].Bet += amount
	if g.seats[seatIdx].ChipsHand == 0 {
		g.seats[seatIdx].AllIn = true
	}
}

func (g *Game) draw() cards.Card {
	c := g.deck[0]
	g.deck = g.deck[1:]
	return c
}

// nextOccupied returns the index of the next non-empty seat after `from`
// (wrapping). Skips empty seats but NOT folded ones — caller filters that.
func (g *Game) nextOccupied(from int) int {
	n := len(g.seats)
	for i := 1; i <= n; i++ {
		idx := (from + i) % n
		if g.seats[idx].UserID != 0 {
			return idx
		}
	}
	return from
}

// ToCall returns the chips seatIdx needs to put in to call the current bet.
func (g *Game) ToCall(seatIdx int) int32 {
	if seatIdx < 0 || seatIdx >= len(g.seats) {
		return 0
	}
	maxBet := int32(0)
	for _, s := range g.seats {
		if s.Bet > maxBet {
			maxBet = s.Bet
		}
	}
	owe := maxBet - g.seats[seatIdx].Bet
	if owe < 0 {
		owe = 0
	}
	return owe
}

// PlayerAction processes one action for the given seat (must be ToAct).
// Returns whether the hand ended.
func (g *Game) PlayerAction(seatIdx int, a Action) bool {
	if seatIdx != g.toAct || g.street == Ended {
		return false
	}
	s := &g.seats[seatIdx]
	toCall := g.ToCall(seatIdx)
	switch a {
	case ActFold:
		s.Folded = true
	case ActCheckCall:
		if toCall > 0 {
			if toCall >= s.ChipsHand {
				s.Bet += s.ChipsHand
				s.ChipsHand = 0
				s.AllIn = true
			} else {
				s.Bet += toCall
				s.ChipsHand -= toCall
			}
		}
	case ActRaise:
		// Call any outstanding bet, then add a raise of 1 BB. Caps at all-in.
		needed := toCall + g.bigBlind
		if needed >= s.ChipsHand {
			s.Bet += s.ChipsHand
			s.ChipsHand = 0
			s.AllIn = true
		} else {
			s.Bet += needed
			s.ChipsHand -= needed
		}
		g.lastRaiserIdx = seatIdx
	case ActAllIn:
		s.Bet += s.ChipsHand
		s.ChipsHand = 0
		s.AllIn = true
		// Treat all-in over the current high bet as a raise.
		if g.ToCall(seatIdx) == 0 && s.Bet > 0 {
			g.lastRaiserIdx = seatIdx
		}
	}
	return g.afterAction()
}

// advanceAction sets toAct to the next non-folded occupied seat after the
// current one. Used by Stand to vacate the actor.
func (g *Game) advanceAction() {
	if g.activeCount() <= 1 {
		g.toAct = -1
		return
	}
	cursor := g.toAct
	for i := 0; i < len(g.seats); i++ {
		cursor = g.nextOccupied(cursor)
		if !g.seats[cursor].Folded && !g.seats[cursor].AllIn {
			g.toAct = cursor
			return
		}
	}
	g.toAct = -1
}

// afterAction advances state: folds reduce active count; once everyone
// either matches the high bet or is all-in, close the street.
func (g *Game) afterAction() bool {
	if g.activeCount() <= 1 {
		g.endByFold()
		return true
	}
	// Walk to next actor.
	cursor := g.toAct
	closed := true
	for i := 0; i < len(g.seats); i++ {
		cursor = g.nextOccupied(cursor)
		if g.seats[cursor].UserID == 0 || g.seats[cursor].Folded || g.seats[cursor].AllIn {
			continue
		}
		// Reached the last raiser without finding a pending actor → street closes.
		if cursor == g.lastRaiserIdx && g.ToCall(cursor) == 0 {
			break
		}
		g.toAct = cursor
		closed = false
		break
	}
	if closed {
		return g.closeStreet()
	}
	return false
}

// closeStreet moves bets into the pot, advances the street, and deals the
// next community card(s). Returns true at end of river (showdown done).
func (g *Game) closeStreet() bool {
	for i := range g.seats {
		g.pot += g.seats[i].Bet
		g.seats[i].Committed += g.seats[i].Bet
		g.seats[i].Bet = 0
	}
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
	// Postflop: action starts left of the button.
	g.toAct = g.firstActiveAfter(g.button)
	g.lastRaiserIdx = g.toAct
	return false
}

func (g *Game) burn() {
	if len(g.deck) > 0 {
		g.deck = g.deck[1:]
	}
}

// firstActiveAfter returns the next non-folded, non-all-in occupied seat
// after `from`. Falls back to the original seat if everyone's all-in.
func (g *Game) firstActiveAfter(from int) int {
	cursor := from
	for i := 0; i < len(g.seats); i++ {
		cursor = g.nextOccupied(cursor)
		if !g.seats[cursor].Folded && !g.seats[cursor].AllIn {
			return cursor
		}
	}
	return from
}

func (g *Game) endByFold() {
	winner := -1
	for i, s := range g.seats {
		if s.UserID != 0 && !s.Folded {
			winner = i
			break
		}
	}
	if winner < 0 {
		g.street = Ended
		return
	}
	// Award entire pot (committed + bets) to winner. Committed += Bet so a
	// fold mid-street still rolls into the same accounting as showdown.
	prize := g.pot
	for i := range g.seats {
		prize += g.seats[i].Bet
		g.seats[i].Committed += g.seats[i].Bet
		g.seats[i].Bet = 0
	}
	g.seats[winner].ChipsHand += prize
	g.lastPayouts[winner] = prize
	g.pot = 0
	g.winner = winner
	g.winRank = 0
	g.street = Ended
}

// resolveShowdown builds the side-pot stack from Committed[seat] and awards
// each layer to the best non-folded hand eligible at that level. Two cases:
//
//   - When everyone committed the same amount (no all-ins, or all-ins that
//     matched the high bet), there's one pot — identical to the original
//     single-pot path.
//   - With short-stack all-ins, we slice the pot into layers: each layer
//     spans from the previous committed level to the next, and only seats
//     that paid into that layer are eligible to win it. The remaining
//     chips of seats who paid more than the layer's cap roll into the
//     next layer.
//
// Folded seats' committed chips fund the pots they paid into but they
// don't compete.
func (g *Game) resolveShowdown() {
	// Collect each seat's total commit + eligibility (non-folded, occupied).
	type seatBucket struct {
		seat      int
		committed int32
		eligible  bool
	}
	buckets := make([]seatBucket, 0, len(g.seats))
	for i, s := range g.seats {
		if s.UserID == 0 {
			continue
		}
		buckets = append(buckets, seatBucket{
			seat:      i,
			committed: s.Committed,
			eligible:  !s.Folded,
		})
	}

	// Unique commitment levels, ascending. Each level → one side pot.
	levelSet := make(map[int32]struct{})
	for _, b := range buckets {
		if b.committed > 0 {
			levelSet[b.committed] = struct{}{}
		}
	}
	levels := make([]int32, 0, len(levelSet))
	for v := range levelSet {
		levels = append(levels, v)
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i] < levels[j] })

	var awards = make(map[int]int32) // seat → chips
	var lastWin int                  // remembers the top-level winner for snapshot
	var lastRank cards.HandRank
	winnerCount := 0

	prev := int32(0)
	for _, lvl := range levels {
		layerSize := lvl - prev
		// Pot for this layer = layerSize × seats that paid at least lvl.
		potChips := int32(0)
		var eligibleSeats []int
		for _, b := range buckets {
			if b.committed >= lvl {
				potChips += layerSize
				if b.eligible {
					eligibleSeats = append(eligibleSeats, b.seat)
				}
			}
		}
		prev = lvl
		if potChips == 0 || len(eligibleSeats) == 0 {
			continue
		}
		// Best hand among eligible seats wins (or chops).
		winners, rank := g.bestAmong(eligibleSeats)
		if len(winners) == 0 {
			continue
		}
		share := potChips / int32(len(winners))
		remainder := potChips % int32(len(winners))
		sort.SliceStable(winners, func(i, j int) bool {
			return seatDistance(winners[i], g.button, len(g.seats)) <
				seatDistance(winners[j], g.button, len(g.seats))
		})
		for i, seat := range winners {
			a := share
			if int32(i) < remainder {
				a++
			}
			awards[seat] += a
		}
		if len(winners) == 1 {
			lastWin = winners[0]
			lastRank = rank
			winnerCount = 1
		} else {
			lastWin = -1
			lastRank = rank
			winnerCount = len(winners)
		}
	}
	for seat, chips := range awards {
		g.seats[seat].ChipsHand += chips
		g.lastPayouts[seat] = chips
	}
	g.pot = 0
	if winnerCount == 1 {
		g.winner = lastWin
		g.winRank = lastRank
	} else {
		g.winner = -1
		g.winRank = lastRank
	}
	g.street = Ended
}

// bestAmong evaluates the seats whose indexes are in `seats` and returns
// the winner indexes (ties → multiple) along with the rank.
func (g *Game) bestAmong(seats []int) ([]int, cards.HandRank) {
	if len(seats) == 0 {
		return nil, 0
	}
	type contender struct {
		seat int
		rank cards.HandRank
		tb   int
	}
	var best []contender
	for _, idx := range seats {
		s := g.seats[idx]
		hand := append([]cards.Card{s.Hole[0], s.Hole[1]}, g.board...)
		r, tb := cards.EvaluateBest(hand)
		c := contender{seat: idx, rank: r, tb: tb}
		if len(best) == 0 || c.rank > best[0].rank || (c.rank == best[0].rank && c.tb > best[0].tb) {
			best = []contender{c}
		} else if c.rank == best[0].rank && c.tb == best[0].tb {
			best = append(best, c)
		}
	}
	out := make([]int, 0, len(best))
	for _, c := range best {
		out = append(out, c.seat)
	}
	return out, best[0].rank
}

// seatDistance returns how many positions clockwise `seat` is from
// `from`+1 (i.e., "first to act" past the button).
func seatDistance(seat, from, n int) int {
	d := (seat - from + n) % n
	if d == 0 {
		return n
	}
	return d
}

// ChipsAtSeat returns the live chip stack for seat idx (0 if empty).
func (g *Game) ChipsAtSeat(seatIdx int) int32 {
	if seatIdx < 0 || seatIdx >= len(g.seats) {
		return 0
	}
	return g.seats[seatIdx].ChipsHand
}

// SeatOfUser returns the seat index occupied by userID, or -1.
func (g *Game) SeatOfUser(userID int64) int {
	for i, s := range g.seats {
		if s.UserID == userID {
			return i
		}
	}
	return -1
}

// Snapshot is the serializable form of the game used to persist tables
// across a server restart. Captures every field that matters for resuming
// — seats with chips, current street, board, pot, blinds, button. The
// active deck + the toAct cursor get reset on restore: any in-flight hand
// is folded forward to "between hands" so the coordinator can resume
// cleanly without trying to reconstruct mid-street betting commitments.
type Snapshot struct {
	Seats      []Seat       `json:"seats"`
	Board      []cards.Card `json:"-"`
	Button     int          `json:"button"`
	Pot        int32        `json:"pot"`
	SmallBlind int32        `json:"sb"`
	BigBlind   int32        `json:"bb"`
	// HandNumber persists the hand counter across restart so the
	// multiplayer_hands unique index on (game_key, table_id, hand_no) doesn't
	// collide with previously-settled hands. Missing on legacy snapshots
	// (zero value) — those tables effectively start fresh.
	HandNumber int64 `json:"hand_no,omitempty"`
}

// SnapshotState dumps the live game into a Snapshot. Called on graceful
// shutdown by the registry.
func (g *Game) SnapshotState() Snapshot {
	snap := Snapshot{
		Seats:      append([]Seat(nil), g.seats...),
		Button:     g.button,
		Pot:        g.pot,
		SmallBlind: g.smallBlind,
		BigBlind:   g.bigBlind,
		HandNumber: g.handNumber,
	}
	// Mid-hand: roll the live bets into ChipsHand so the restore is a clean
	// "between hands" state. Avoids having to reconstruct deck + toAct
	// cursor across the restart boundary.
	for i := range snap.Seats {
		snap.Seats[i].ChipsHand += snap.Seats[i].Bet + snap.Seats[i].Committed
		snap.Seats[i].Bet = 0
		snap.Seats[i].Committed = 0
		snap.Seats[i].Folded = false
		snap.Seats[i].AllIn = false
		snap.Seats[i].Hole = [2]cards.Card{}
	}
	return snap
}

// RestoreFromSnapshot rehydrates a Game from a previously-saved Snapshot.
// The result is in "Ended" street — the coordinator will auto-start the
// next hand as soon as ≥2 seats are still occupied.
func RestoreFromSnapshot(s Snapshot, rng doors.CryptoRng) *Game {
	cap := len(s.Seats)
	if cap < 2 {
		cap = 2
	}
	if cap > 9 {
		cap = 9
	}
	g := NewGame(rng, cap, s.SmallBlind, s.BigBlind)
	for i := 0; i < cap && i < len(s.Seats); i++ {
		seat := s.Seats[i]
		if seat.UserID != 0 && seat.ChipsHand > 0 {
			g.seats[i] = Seat{
				UserID:    seat.UserID,
				Handle:    seat.Handle,
				ChipsHand: seat.ChipsHand,
			}
		}
	}
	g.button = s.Button
	g.pot = 0
	g.street = Ended
	g.toAct = -1
	g.winner = -1
	g.handNumber = s.HandNumber
	return g
}
