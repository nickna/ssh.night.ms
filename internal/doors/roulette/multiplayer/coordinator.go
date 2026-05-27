package multiplayer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nickna/ssh.night.ms/internal/doors"
	gamesmp "github.com/nickna/ssh.night.ms/internal/doors/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
)

// GameKey is the value coordinator writes into multiplayer_hands.game_key
// and game_rounds.game_key — keeps leaderboards able to disambiguate
// roulette wins from Hold'em or slots.
const GameKey = "roulette"

// PhaseDurations configures how long each phase of one spin cycle lasts.
// DefaultPhaseDurations matches the plan: 30s betting → 1s no-more-bets →
// 5s spinning → 8s reveal → loop. Tests pass tiny durations (~50ms) so the
// whole cycle finishes inside an assertion-deadline-friendly window.
type PhaseDurations struct {
	Betting    time.Duration
	NoMoreBets time.Duration
	Spinning   time.Duration
	Reveal     time.Duration
}

// DefaultPhaseDurations matches the deployed cadence.
var DefaultPhaseDurations = PhaseDurations{
	Betting:    30 * time.Second,
	NoMoreBets: 1 * time.Second,
	Spinning:   5 * time.Second,
	Reveal:     8 * time.Second,
}

// DefaultLastBetCutoff is the cushion before EndsAt where new bets are
// rejected. Cheap defence against Redis-hop latency on a bet placed in the
// final tens of milliseconds of the window.
const DefaultLastBetCutoff = 500 * time.Millisecond

// Errors PlaceBet may return.
var (
	ErrBettingClosed = errors.New("roulette: betting closed for this round")
	ErrZeroBet       = errors.New("roulette: bet amount must be positive")
)

// Rng is the random-pocket source the coordinator uses to draw the winning
// pocket on Spinning entry. Production passes doors.CryptoRng{}; tests pass
// a stub that returns deterministic indices.
type Rng interface {
	Intn(n int) int
}

// Wallet is the per-user wallet contract the coordinator depends on. The
// production binding wraps doors.WalletService so each call does its own
// atomic Load + Bet / Load + Credit cycle. Tests stub this directly.
type Wallet interface {
	// DebitBet atomically debits amount from the user's wallet. Returns the
	// underlying ErrInsufficient on insufficient funds.
	DebitBet(ctx context.Context, userID int64, amount int32) error
	// Credit atomically credits amount to the user's wallet (used for both
	// winning payouts and shutdown refunds of in-flight bets).
	Credit(ctx context.Context, userID int64, amount int64) error
}

// WalletAdapter wires the production doors.WalletService into the Wallet
// interface. Each call does its own Load → mutate → persist round-trip;
// safe because doors.WalletService.Bet / Credit are atomic per-call.
type WalletAdapter struct {
	Svc *doors.WalletService
}

// DebitBet loads the user's wallet and debits amount. Returns the wrapped
// doors.ErrInsufficient unchanged so callers can type-check.
func (a *WalletAdapter) DebitBet(ctx context.Context, userID int64, amount int32) error {
	if amount <= 0 {
		return ErrZeroBet
	}
	w, err := a.Svc.Load(ctx, userID)
	if err != nil {
		return err
	}
	return a.Svc.Bet(ctx, &w, amount)
}

// Credit loads the user's wallet and credits amount.
func (a *WalletAdapter) Credit(ctx context.Context, userID int64, amount int64) error {
	if amount <= 0 {
		return nil
	}
	w, err := a.Svc.Load(ctx, userID)
	if err != nil {
		return err
	}
	return a.Svc.Credit(ctx, &w, amount)
}

// Config gathers everything the coordinator needs at construction time.
// Wallet may be nil in tests that don't need wallet plumbing — PlaceBet
// will then debit nothing and the ledger settles with zero credits. Ledger
// nil disables the audit trail.
type Config struct {
	Durations     PhaseDurations
	LastBetCutoff time.Duration
	Wallet        Wallet
	Ledger        gamesmp.Ledger
	Rng           Rng
	Logger        *slog.Logger
}

// Coordinator is the actor that owns the global roulette table. The actor
// goroutine is the only mutator of phase-machine state; every external call
// is funnelled through the cmds channel so reads are serialised against
// the deadline timer.
type Coordinator struct {
	cfg    Config
	cmds   chan command
	subsMu sync.Mutex
	subs   []*subscriber

	// Authoritative state — only the actor goroutine mutates these.
	phase       Phase
	endsAt      time.Time
	phaseToken  int64
	winning     *roulette.Pocket
	pendingBets []userBet
	aggregate   map[roulette.BetKey]int32
	history     historyRing
	handNumber  int64 // monotonic round counter for ledger HandNumber

	// onShutdownRefund is set by the registry; called for each in-flight bet
	// when ctx cancels so users don't lose chips placed pre-shutdown.
	onShutdownRefund func(ub userBet)
}

// userBet is one wager record + the user that owns it, kept so we can credit
// individual winners at reveal time. Coordinator never reveals who-bet-what
// over the broadcast channel — only the aggregate by BetKey is published.
type userBet struct {
	UserID int64
	Handle string
	Bet    roulette.Bet
}

// command is the union of operations the coordinator handles. Each carries a
// reply channel so callers can wait for the result without grabbing locks.
type command struct {
	kind cmdKind

	// PlaceBet fields
	userID int64
	handle string
	bet    roulette.Bet

	// Subscribe fields
	sub *subscriber

	// Snapshot reply channel
	snapReply chan PhaseMsg

	reply chan cmdReply
}

type cmdKind int

const (
	cmdPlaceBet cmdKind = iota
	cmdSubscribe
	cmdUnsubscribe
	cmdSnapshot
	cmdPersistDrain
	cmdPersistShape
)

type cmdReply struct {
	err   error
	bets  []userBet    // for cmdPersistDrain
	shape PersistShape // for cmdPersistShape
}

// subscriber is one per-session view binding.
type subscriber struct {
	send   chan PhaseMsg
	closed bool
}

// NewCoordinator constructs the singleton coordinator. Caller is expected
// to immediately go coord.Run(ctx); commands queued before Run starts will
// block until the actor loop drains them.
func NewCoordinator(cfg Config) *Coordinator {
	if cfg.Durations == (PhaseDurations{}) {
		cfg.Durations = DefaultPhaseDurations
	}
	if cfg.LastBetCutoff == 0 {
		cfg.LastBetCutoff = DefaultLastBetCutoff
	}
	if cfg.Rng == nil {
		cfg.Rng = doors.CryptoRng{}
	}
	return &Coordinator{
		cfg:       cfg,
		cmds:      make(chan command, 32),
		aggregate: make(map[roulette.BetKey]int32),
	}
}

// SetOnShutdownRefund wires a registry-supplied closure called once per
// in-flight bet during Persist drain. Must be set before Run.
func (c *Coordinator) SetOnShutdownRefund(fn func(ub userBet)) {
	c.onShutdownRefund = fn
}

// Run is the actor loop. Returns when ctx cancels or after a final
// shutdownSubs sweep.
func (c *Coordinator) Run(ctx context.Context) {
	c.startBetting(time.Now())
	for {
		// Time until the active phase's deadline. Negative durations mean
		// the deadline already passed; advancePhase handles that case.
		now := time.Now()
		remaining := c.endsAt.Sub(now)
		if remaining < 0 {
			remaining = 0
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.drainAndRefund()
			c.shutdownSubs()
			return
		case <-timer.C:
			c.advancePhase(time.Now())
		case cm, ok := <-c.cmds:
			timer.Stop()
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
	case cmdPlaceBet:
		err := c.handlePlaceBet(cm.userID, cm.handle, cm.bet, time.Now())
		cm.reply <- cmdReply{err: err}
		if err == nil {
			c.broadcast()
		}
	case cmdSubscribe:
		c.subsMu.Lock()
		c.subs = append(c.subs, cm.sub)
		c.subsMu.Unlock()
		// Push an initial snapshot so the new client paints immediately.
		select {
		case cm.sub.send <- c.buildPhaseMsg():
		default:
		}
		cm.reply <- cmdReply{}
	case cmdUnsubscribe:
		c.removeSub(cm.sub)
		cm.reply <- cmdReply{}
	case cmdSnapshot:
		cm.snapReply <- c.buildPhaseMsg()
	case cmdPersistDrain:
		bets := append([]userBet(nil), c.pendingBets...)
		c.pendingBets = nil
		c.aggregate = make(map[roulette.BetKey]int32)
		cm.reply <- cmdReply{bets: bets}
	case cmdPersistShape:
		cm.reply <- cmdReply{shape: PersistShape{
			History:    c.history.All(),
			PhaseToken: c.phaseToken,
		}}
	}
}

func (c *Coordinator) handlePlaceBet(userID int64, handle string, bet roulette.Bet, now time.Time) error {
	if bet.Amount <= 0 {
		return ErrZeroBet
	}
	if c.phase != PhaseBetting {
		return ErrBettingClosed
	}
	if c.endsAt.Sub(now) <= c.cfg.LastBetCutoff {
		return ErrBettingClosed
	}
	if c.cfg.Wallet != nil {
		// Best-effort: 3s timeout so a stalled DB doesn't park the actor.
		// The wallet's persist is atomic at the SQL layer; on success the
		// chips are debited before we record the bet.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.cfg.Wallet.DebitBet(ctx, userID, bet.Amount); err != nil {
			return err
		}
	}
	c.pendingBets = append(c.pendingBets, userBet{UserID: userID, Handle: handle, Bet: bet})
	c.aggregate[bet.Key] += bet.Amount
	return nil
}

// startBetting moves the machine into a fresh Betting phase: clears the
// aggregate, increments the phase token, and arms the deadline.
func (c *Coordinator) startBetting(now time.Time) {
	c.phase = PhaseBetting
	c.winning = nil
	c.pendingBets = nil
	c.aggregate = make(map[roulette.BetKey]int32)
	c.handNumber++
	c.phaseToken++
	c.endsAt = now.Add(c.cfg.Durations.Betting)
	c.broadcast()
}

// advancePhase progresses the state machine on a deadline tick.
func (c *Coordinator) advancePhase(now time.Time) {
	switch c.phase {
	case PhaseBetting:
		c.phase = PhaseNoMoreBets
		c.phaseToken++
		c.endsAt = now.Add(c.cfg.Durations.NoMoreBets)
		c.broadcast()
	case PhaseNoMoreBets:
		// Draw the winning pocket once, here, on the actor goroutine. The
		// value is included in the Spinning broadcast so every client
		// animates toward the same pocket.
		p := roulette.Draw(c.cfg.Rng)
		c.winning = &p
		c.phase = PhaseSpinning
		c.phaseToken++
		c.endsAt = now.Add(c.cfg.Durations.Spinning)
		c.broadcast()
	case PhaseSpinning:
		c.phase = PhaseReveal
		c.phaseToken++
		c.endsAt = now.Add(c.cfg.Durations.Reveal)
		c.settleRound()
		c.broadcast()
	case PhaseReveal:
		c.startBetting(now)
	}
}

// settleRound credits winning bets and writes the ledger row. Runs on the
// actor goroutine so it is serialised with PlaceBet — no double-spend race.
// Ledger writes happen in a fresh goroutine so a stalled Postgres can't
// freeze the actor.
func (c *Coordinator) settleRound() {
	if c.winning == nil {
		return
	}
	p := *c.winning
	movements := make(map[int64]*gamesmp.PlayerMovement)
	var perBetDetails []map[string]any
	for _, ub := range c.pendingBets {
		gross := roulette.GrossReturn(p, ub.Bet)
		net := gross - ub.Bet.Amount
		// Stake was already debited at place-time; credit the gross return
		// (stake + winnings) on a win so the wallet sees its chips back plus
		// the payout. On a loss gross == 0 and there's nothing to credit.
		if gross > 0 && c.cfg.Wallet != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := c.cfg.Wallet.Credit(ctx, ub.UserID, int64(gross)); err != nil && c.cfg.Logger != nil {
				c.cfg.Logger.Warn("roulette: credit failed", "user", ub.UserID, "amount", gross, "err", err)
			}
			cancel()
		}
		mv, ok := movements[ub.UserID]
		if !ok {
			mv = &gamesmp.PlayerMovement{UserID: ub.UserID, Handle: ub.Handle}
			movements[ub.UserID] = mv
		}
		mv.Wagered += ub.Bet.Amount
		mv.Payout += gross
		_ = net // each player's net = sum(Payout) - sum(Wagered) at row-build time
		perBetDetails = append(perBetDetails, map[string]any{
			"user":   ub.UserID,
			"handle": ub.Handle,
			"type":   ub.Bet.Key.Type.String(),
			"number": ub.Bet.Key.Number.Number(),
			"amount": ub.Bet.Amount,
			"gross":  gross,
		})
	}
	c.history.Push(p)

	if c.cfg.Ledger != nil && len(movements) > 0 {
		settlement := gamesmp.Settlement{
			GameKey:    GameKey,
			HandNumber: c.handNumber,
			TableID:    1, // singleton global table
		}
		for _, mv := range movements {
			settlement.Movements = append(settlement.Movements, *mv)
		}
		details, err := json.Marshal(map[string]any{
			"winning_pocket": p.Number(),
			"color":          p.Color().String(),
			"bets":           perBetDetails,
		})
		if err != nil {
			details = []byte("{}")
		}
		settlement.Details = details
		ledger := c.cfg.Ledger
		logger := c.cfg.Logger
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := ledger.SettleHand(ctx, settlement); err != nil && logger != nil {
				logger.Warn("roulette: ledger settle failed", "hand", settlement.HandNumber, "err", err)
			}
		}()
	}
}

// drainAndRefund refunds every in-flight bet so users don't lose chips that
// were placed but never settled (graceful-shutdown path). Runs on the actor
// goroutine inside Run's ctx-cancel handler — safe to mutate state.
func (c *Coordinator) drainAndRefund() {
	if c.onShutdownRefund == nil {
		return
	}
	for _, ub := range c.pendingBets {
		c.onShutdownRefund(ub)
	}
	c.pendingBets = nil
}

// buildPhaseMsg constructs a fresh broadcast message from the current state.
// Called both for proactive broadcasts and one-shot snapshot replies.
func (c *Coordinator) buildPhaseMsg() PhaseMsg {
	c.subsMu.Lock()
	occ := len(c.subs)
	c.subsMu.Unlock()
	agg := make(map[string]int32, len(c.aggregate))
	for k, v := range c.aggregate {
		agg[k.String()] = v
	}
	var winning *roulette.Pocket
	if c.winning != nil {
		w := *c.winning
		winning = &w
	}
	return PhaseMsg{
		Phase:      c.phase,
		EndsAt:     c.endsAt,
		PhaseToken: c.phaseToken,
		Winning:    winning,
		Aggregate:  agg,
		History:    c.history.Slice(10),
		Occupants:  occ,
	}
}

// broadcast fans the current state out to every subscriber. Non-blocking:
// a slow subscriber misses a snapshot rather than back-pressuring the actor.
func (c *Coordinator) broadcast() {
	msg := c.buildPhaseMsg()
	c.subsMu.Lock()
	subs := make([]*subscriber, len(c.subs))
	copy(subs, c.subs)
	c.subsMu.Unlock()
	for _, s := range subs {
		select {
		case s.send <- msg:
		default:
		}
	}
}

// Subscribe registers a per-session listener. The returned channel receives
// every PhaseMsg the coordinator publishes; the cancel func unsubscribes.
func (c *Coordinator) Subscribe() (<-chan PhaseMsg, func()) {
	sub := &subscriber{send: make(chan PhaseMsg, 8)}
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdSubscribe, sub: sub, reply: reply}:
		<-reply
	case <-time.After(2 * time.Second):
		// Actor wedged; return a dead channel so the screen gracefully shows
		// a "no live updates" state rather than spinning.
		close(sub.send)
		return sub.send, func() {}
	}
	return sub.send, func() {
		ureply := make(chan cmdReply, 1)
		select {
		case c.cmds <- command{kind: cmdUnsubscribe, sub: sub, reply: ureply}:
			<-ureply
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *Coordinator) removeSub(sub *subscriber) {
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

func (c *Coordinator) shutdownSubs() {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	for _, s := range c.subs {
		if !s.closed {
			s.closed = true
			close(s.send)
		}
	}
	c.subs = nil
}

// PlaceBet asks the coordinator to record a wager for userID. Returns
// ErrBettingClosed if outside the betting window, ErrZeroBet for an
// invalid amount, or doors.ErrInsufficient if the wallet debit fails.
func (c *Coordinator) PlaceBet(userID int64, handle string, bet roulette.Bet) error {
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdPlaceBet, userID: userID, handle: handle, bet: bet, reply: reply}:
		r := <-reply
		return r.err
	case <-time.After(2 * time.Second):
		return errors.New("roulette: coordinator busy")
	}
}

// Snapshot returns the current PhaseMsg synchronously. Used by the screen
// to populate its first paint without waiting for the next broadcast tick.
func (c *Coordinator) Snapshot() PhaseMsg {
	reply := make(chan PhaseMsg, 1)
	select {
	case c.cmds <- command{kind: cmdSnapshot, snapReply: reply}:
		return <-reply
	case <-time.After(2 * time.Second):
		return PhaseMsg{}
	}
}

// History returns a copy of the rolling outcome list, oldest-first.
func (c *Coordinator) History() []roulette.Pocket {
	snap := c.Snapshot()
	if snap.History == nil {
		return nil
	}
	out := make([]roulette.Pocket, len(snap.History))
	copy(out, snap.History)
	return out
}

// PersistShape returns the durable subset of state (history + phase token)
// for the registry to JSON-marshal into roulette_state.snapshot. Pull is
// actor-serialised so it neither races with Push nor reads partial state.
// Returns the zero value if the actor is no longer running.
func (c *Coordinator) PersistShape() PersistShape {
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdPersistShape, reply: reply}:
		return (<-reply).shape
	case <-time.After(2 * time.Second):
		return PersistShape{}
	}
}

// ApplyPersistShape rehydrates the coordinator from a serialised snapshot.
// Must be called before Run; modifies state directly without the actor loop.
func (c *Coordinator) ApplyPersistShape(s PersistShape) {
	c.history.Replace(s.History)
	if s.PhaseToken > c.phaseToken {
		c.phaseToken = s.PhaseToken
	}
}

// DrainPending is the registry's escape hatch for graceful shutdown — pulls
// the current pendingBets list off the actor and zeros it. The registry's
// caller refunds each via WalletService.Credit before persistence finishes.
// Safe to call while the actor is still running.
func (c *Coordinator) DrainPending() []userBet {
	reply := make(chan cmdReply, 1)
	select {
	case c.cmds <- command{kind: cmdPersistDrain, reply: reply}:
		r := <-reply
		return r.bets
	case <-time.After(2 * time.Second):
		return nil
	}
}

// ComputeStats returns the hot/cold/streak rollup over the full history
// ring. Cheap (≤100 entries) so screens can call this on every paint.
func (c *Coordinator) ComputeStats() Stats { return c.history.ComputeStats() }

