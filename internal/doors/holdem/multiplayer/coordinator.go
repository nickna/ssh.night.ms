package multiplayer

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickna/ssh.night.ms/internal/doors"
	"github.com/nickna/ssh.night.ms/internal/doors/cards"
	gamesmp "github.com/nickna/ssh.night.ms/internal/doors/multiplayer"
)

// TableSnapshot is the broadcast wire-shape. Coordinator publishes one after
// every state mutation. Per-seat hole cards are masked except for the seat's
// own user and showdown. The screen renders directly from the snapshot.
type TableSnapshot struct {
	TableID      int64
	Seats        []SeatView
	Board        []cards.Card
	Pot          int32
	Street       Street
	ToAct        int
	Button       int
	BigBlind     int32
	SmallBlind   int32
	Winner       int
	WinRank      cards.HandRank
	HandRunning  bool
	CapSeats     int
	OccupiedSeats int
}

// SeatView is one seat as visible to a particular session. Hole is zero
// when the seat isn't yours and the hand isn't at showdown.
type SeatView struct {
	UserID    int64
	Handle    string
	ChipsHand int32
	Bet       int32
	Hole      [2]cards.Card
	HoleShown bool
	Folded    bool
	AllIn     bool
}

// Coordinator is the per-table actor. It owns the only mutable reference to
// the Game; every read/write goes through its in/out channels. Run() is the
// goroutine entry point; Close() stops it.
type Coordinator struct {
	TableID int64
	Name    string

	cmds      chan command
	game      *Game
	subs      []*subscriber
	subsMu    sync.Mutex
	closed    atomic.Bool

	// onSettlement fires once per settled hand. The registry wires it to a
	// closure that calls the multiplayer ledger to persist multiplayer_hands
	// + per-human game_rounds rows. Nil-safe — coordinators created in
	// tests skip persistence cleanly.
	onSettlement func(s Settlement)
}

// PlayerMovement / Settlement / Ledger are aliases for the shared
// internal/doors/multiplayer types — lifted out of this package so a second
// multiplayer game (roulette) can write multiplayer_hands rows without
// importing the Hold'em coordinator. Call sites keep using the original
// names; the alias makes the move source-compatible.
type PlayerMovement = gamesmp.PlayerMovement
type Settlement = gamesmp.Settlement
type Ledger = gamesmp.Ledger

// command is the union of operations the coordinator processes. Each carries
// a reply chan so callers can wait for the result without locking the
// coordinator's state.
type command struct {
	kind cmdKind
	user int64

	// Sit fields
	seatIdx int
	handle  string
	chips   int32

	// Action fields
	action Action

	reply chan cmdReply
}

type cmdKind int

const (
	cmdSit cmdKind = iota
	cmdStand
	cmdAction
	cmdStartHand
	cmdSnapshot
)

type cmdReply struct {
	ok       bool
	err      string
	chips    int32 // for stand: chips returned
	snapshot TableSnapshot
}

// subscriber is one per-session view binding. send is the buffered output
// channel; the seatUser determines what hole cards are visible.
type subscriber struct {
	id       int64
	seatUser int64
	send     chan TableSnapshot
	closed   bool
}

// NewCoordinator builds a fresh table.
func NewCoordinator(tableID int64, name string, capSeats int, sb, bb int32) *Coordinator {
	return &Coordinator{
		TableID: tableID,
		Name:    name,
		cmds:    make(chan command, 16),
		game:    NewGame(doors.CryptoRng{}, capSeats, sb, bb),
	}
}

// SetOnSettlement wires the post-hand hook. Must be set before Run.
func (c *Coordinator) SetOnSettlement(fn func(s Settlement)) { c.onSettlement = fn }

// Run is the actor loop. Returns when ctx is cancelled or Close() is called.
func (c *Coordinator) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			c.shutdownSubs()
			return
		case cm, ok := <-c.cmds:
			if !ok {
				c.shutdownSubs()
				return
			}
			c.process(cm)
		}
	}
}

func (c *Coordinator) process(cm command) {
	switch cm.kind {
	case cmdSit:
		ok := c.game.Sit(cm.seatIdx, cm.user, cm.handle, cm.chips)
		cm.reply <- cmdReply{ok: ok}
		if ok {
			c.maybeStart()
			c.broadcast()
		}
	case cmdStand:
		seat := c.game.SeatOfUser(cm.user)
		if seat < 0 {
			cm.reply <- cmdReply{ok: false, err: "not seated"}
			return
		}
		chips := c.game.Stand(seat, cm.user)
		cm.reply <- cmdReply{ok: true, chips: chips}
		c.broadcast()
	case cmdAction:
		seat := c.game.SeatOfUser(cm.user)
		if seat < 0 {
			cm.reply <- cmdReply{ok: false, err: "not seated"}
			return
		}
		ended := c.game.PlayerAction(seat, cm.action)
		cm.reply <- cmdReply{ok: true}
		c.broadcast()
		if ended {
			c.handleEnd()
		}
	case cmdStartHand:
		ok := c.game.StartHand()
		cm.reply <- cmdReply{ok: ok}
		if ok {
			c.broadcast()
		}
	case cmdSnapshot:
		cm.reply <- cmdReply{ok: true, snapshot: c.snapshotFor(cm.user)}
	}
}

// maybeStart starts a hand when at least 2 seats are filled and we're idle.
// Hands run back-to-back inside the actor loop with a short pause baked
// in via the broadcast cadence.
func (c *Coordinator) maybeStart() {
	if c.game.Street() != Ended {
		return
	}
	if len(c.game.OccupiedSeats()) < 2 {
		return
	}
	c.game.StartHand()
}

// handleEnd handles post-showdown bookkeeping: fire onSettlement so the
// registry can write multiplayer_hands + per-human game_rounds, then auto-
// start the next hand after a brief pause so the screen has time to render
// the showdown.
func (c *Coordinator) handleEnd() {
	if c.onSettlement != nil {
		c.onSettlement(c.buildSettlement())
	}
	// Auto-start next hand. Sleep happens off the actor goroutine via a
	// scheduled cmd so we don't block other commands.
	go func() {
		time.Sleep(3 * time.Second)
		if c.closed.Load() {
			return
		}
		reply := make(chan cmdReply, 1)
		select {
		case c.cmds <- command{kind: cmdStartHand, reply: reply}:
			<-reply
		case <-time.After(time.Second):
			// Coordinator died mid-pause; harmless.
		}
	}()
}

// Subscribe registers a view binding for the seat owned by userID (or 0 for
// a spectator). Returns the channel of snapshots and a cancel func.
func (c *Coordinator) Subscribe(userID int64) (<-chan TableSnapshot, func()) {
	sub := &subscriber{
		seatUser: userID,
		send:     make(chan TableSnapshot, 4),
	}
	c.subsMu.Lock()
	sub.id = int64(len(c.subs) + 1)
	c.subs = append(c.subs, sub)
	c.subsMu.Unlock()
	// Push the initial snapshot async so Subscribe returns fast.
	go func() {
		reply := make(chan cmdReply, 1)
		select {
		case c.cmds <- command{kind: cmdSnapshot, user: userID, reply: reply}:
			r := <-reply
			select {
			case sub.send <- r.snapshot:
			default:
			}
		case <-time.After(2 * time.Second):
		}
	}()
	return sub.send, func() { c.unsubscribe(sub) }
}

func (c *Coordinator) unsubscribe(sub *subscriber) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	if sub.closed {
		return
	}
	sub.closed = true
	close(sub.send)
	for i, s := range c.subs {
		if s == sub {
			c.subs = append(c.subs[:i], c.subs[i+1:]...)
			return
		}
	}
}

// broadcast pushes a snapshot to every subscriber, masked by their seatUser.
// Non-blocking: a slow subscriber misses snapshots rather than backpressuring
// the actor.
func (c *Coordinator) broadcast() {
	c.subsMu.Lock()
	subs := make([]*subscriber, len(c.subs))
	copy(subs, c.subs)
	c.subsMu.Unlock()
	for _, sub := range subs {
		snap := c.snapshotFor(sub.seatUser)
		select {
		case sub.send <- snap:
		default:
		}
	}
}

func (c *Coordinator) shutdownSubs() {
	c.closed.Store(true)
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	for _, sub := range c.subs {
		if !sub.closed {
			sub.closed = true
			close(sub.send)
		}
	}
	c.subs = nil
}

// snapshotFor builds a TableSnapshot with hole cards masked for everyone
// other than viewerUserID, except at showdown when everyone sees everything.
func (c *Coordinator) snapshotFor(viewerUserID int64) TableSnapshot {
	seats := c.game.Seats()
	view := make([]SeatView, len(seats))
	atShowdown := c.game.Street() == Showdown || c.game.Street() == Ended
	for i, s := range seats {
		v := SeatView{
			UserID:    s.UserID,
			Handle:    s.Handle,
			ChipsHand: s.ChipsHand,
			Bet:       s.Bet,
			Folded:    s.Folded,
			AllIn:     s.AllIn,
		}
		if s.UserID == viewerUserID && viewerUserID != 0 {
			v.Hole = s.Hole
			v.HoleShown = true
		} else if atShowdown && s.UserID != 0 && !s.Folded {
			v.Hole = s.Hole
			v.HoleShown = true
		}
		view[i] = v
	}
	return TableSnapshot{
		TableID:       c.TableID,
		Seats:         view,
		Board:         c.game.Board(),
		Pot:           c.game.Pot(),
		Street:        c.game.Street(),
		ToAct:         c.game.ToAct(),
		Button:        c.game.Button(),
		BigBlind:      c.game.BigBlind(),
		SmallBlind:    c.game.BigBlind() / 2,
		Winner:        c.game.Winner(),
		WinRank:       c.game.WinRank(),
		HandRunning:   c.game.Street() != Ended,
		CapSeats:      c.game.SeatCount(),
		OccupiedSeats: len(c.game.OccupiedSeats()),
	}
}

// buildSettlement assembles the per-hand audit payload the ledger persists.
// Pulls Committed (total wagered) + Payouts from the engine, walks every
// seat once, skips empty ones, and emits one PlayerMovement per occupant
// (CPUs included; the ledger filters them at write time so log lines stay
// useful for debugging table activity).
//
// Details JSON intentionally matches the .NET shape — board as compact
// "As/Td/5c" strings, payouts as {SeatIndex, Amount, Reason} — so a hand-
// replay tool that decodes either stack's audit trail sees identical
// fields. Reason is always "showdown" or "fold-walkover" on the Go side
// (the engine doesn't track .NET's per-pot reason strings).
func (c *Coordinator) buildSettlement() Settlement {
	seats := c.game.Seats()
	payouts := c.game.Payouts()
	movements := make([]PlayerMovement, 0, len(seats))
	for i, s := range seats {
		if s.UserID == 0 {
			continue
		}
		wagered := s.Committed
		var payout int32
		if i < len(payouts) {
			payout = payouts[i]
		}
		if wagered == 0 && payout == 0 {
			continue
		}
		movements = append(movements, PlayerMovement{
			UserID:  s.UserID,
			Handle:  s.Handle,
			Wagered: wagered,
			Payout:  payout,
			Stack:   s.ChipsHand,
		})
	}

	reason := "fold-walkover"
	if len(c.game.Board()) == 5 {
		reason = "showdown"
	}
	type payoutWire struct {
		SeatIndex int    `json:"SeatIndex"`
		Amount    int32  `json:"Amount"`
		Reason    string `json:"Reason"`
	}
	var pw []payoutWire
	for i, amount := range payouts {
		if amount > 0 {
			pw = append(pw, payoutWire{SeatIndex: i, Amount: amount, Reason: reason})
		}
	}
	board := make([]string, 0, len(c.game.Board()))
	for _, card := range c.game.Board() {
		board = append(board, encodeCardWire(card))
	}
	details, err := json.Marshal(map[string]any{
		"board":   board,
		"payouts": pw,
	})
	if err != nil {
		// json.Marshal on a fixed-shape map can fail only on a programmer
		// bug (NaN floats etc.). Surface as empty JSON so the parent row
		// still writes and the leaderboard still gets the per-human rows.
		details = []byte("{}")
	}

	return Settlement{
		TableID:    c.TableID,
		GameKey:    "holdem-mp",
		HandNumber: c.game.HandNumber(),
		Movements:  movements,
		Details:    details,
	}
}

// encodeCardWire renders a card as the .NET wire format ("As", "Td", "5c")
// so hand-history JSON is decodable by tooling written against either
// stack. Suits are lower-case so the glyph stays one column.
func encodeCardWire(c cards.Card) string {
	const rankStr = "  23456789TJQKA"
	r := rune(rankStr[c.Rank])
	rankPart := string(r)
	if c.Rank == cards.Ten {
		rankPart = "10"
	}
	suit := "?"
	switch c.Suit {
	case cards.Clubs:
		suit = "c"
	case cards.Diamonds:
		suit = "d"
	case cards.Hearts:
		suit = "h"
	case cards.Spades:
		suit = "s"
	}
	return rankPart + suit
}

// Sit posts a Sit command. Returns whether the seat was taken successfully.
func (c *Coordinator) Sit(userID int64, handle string, seatIdx int, chips int32) bool {
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdSit, user: userID, seatIdx: seatIdx, handle: handle, chips: chips, reply: reply}:
		r := <-reply
		return r.ok
	case <-time.After(2 * time.Second):
		return false
	}
}

// Stand removes a seated user. Returns the chips they leave with.
func (c *Coordinator) Stand(userID int64) int32 {
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdStand, user: userID, reply: reply}:
		r := <-reply
		return r.chips
	case <-time.After(2 * time.Second):
		return 0
	}
}

// Action posts a single seat action. ok=false means the action wasn't legal
// (not seated, not to-act, etc.).
func (c *Coordinator) Action(userID int64, a Action) bool {
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdAction, user: userID, action: a, reply: reply}:
		r := <-reply
		return r.ok
	case <-time.After(2 * time.Second):
		return false
	}
}
