package multiplayer

import (
	"context"
	"sync"
	"testing"
	"time"

	gamesmp "github.com/nickna/ssh.night.ms/internal/doors/multiplayer"
	"github.com/nickna/ssh.night.ms/internal/doors/roulette"
)

// fastDurations runs the whole spin cycle in well under a second so tests
// observe state transitions without sleeping long. Cutoff is tightened so
// PlaceBet's last-N-ms guard takes effect within the betting window.
var fastDurations = PhaseDurations{
	Betting:    150 * time.Millisecond,
	NoMoreBets: 30 * time.Millisecond,
	Spinning:   60 * time.Millisecond,
	Reveal:     60 * time.Millisecond,
}

// fixedRng always returns the same pocket index. Lets tests force the
// winning pocket and verify settlement math.
type fixedRng struct{ val int }

func (r fixedRng) Intn(n int) int { return r.val }

// recordingWallet captures every DebitBet / Credit call so tests can assert
// the exact wallet activity per round. Successful debits enforce the
// configured balance limit.
type recordingWallet struct {
	mu       sync.Mutex
	balance  map[int64]int32
	debits   []walletCall
	credits  []walletCall
	debitErr error
}

type walletCall struct {
	user   int64
	amount int64
}

func newRecordingWallet() *recordingWallet {
	return &recordingWallet{balance: map[int64]int32{}}
}

func (w *recordingWallet) seed(user int64, amount int32) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.balance[user] = amount
}

func (w *recordingWallet) DebitBet(_ context.Context, userID int64, amount int32) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.debitErr != nil {
		return w.debitErr
	}
	if w.balance[userID] < amount {
		return errInsufficient
	}
	w.balance[userID] -= amount
	w.debits = append(w.debits, walletCall{user: userID, amount: int64(amount)})
	return nil
}

func (w *recordingWallet) Credit(_ context.Context, userID int64, amount int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.balance[userID] += int32(amount)
	w.credits = append(w.credits, walletCall{user: userID, amount: amount})
	return nil
}

var errInsufficient = sentinelErr("insufficient")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

// recordingLedger snapshots every SettleHand call so tests can verify the
// audit trail.
type recordingLedger struct {
	mu          sync.Mutex
	settlements []gamesmp.Settlement
}

func (l *recordingLedger) SettleHand(_ context.Context, s gamesmp.Settlement) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settlements = append(l.settlements, s)
	return nil
}

// waitFor consumes phase messages from sub until pred returns true or
// timeout elapses. Returns the matching PhaseMsg on success.
func waitFor(t *testing.T, sub <-chan PhaseMsg, pred func(PhaseMsg) bool, timeout time.Duration) PhaseMsg {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-sub:
			if !ok {
				t.Fatalf("waitFor: subscription closed before predicate matched")
			}
			if pred(msg) {
				return msg
			}
		case <-deadline:
			t.Fatalf("waitFor: timeout after %s", timeout)
		}
	}
}

func TestPhaseProgression(t *testing.T) {
	w := newRecordingWallet()
	w.seed(1, 100)
	c := NewCoordinator(Config{
		Durations:     fastDurations,
		LastBetCutoff: 10 * time.Millisecond,
		Wallet:        w,
		Rng:           fixedRng{val: 17},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	sub, unsub := c.Subscribe()
	defer unsub()

	// Initial snapshot should be Betting with a future EndsAt.
	first := <-sub
	if first.Phase != PhaseBetting {
		t.Fatalf("initial phase want Betting, got %s", first.Phase)
	}

	// Advance through the cycle. We expect to see NoMoreBets, Spinning,
	// Reveal, Betting again.
	wantSeq := []Phase{PhaseNoMoreBets, PhaseSpinning, PhaseReveal, PhaseBetting}
	for _, want := range wantSeq {
		got := waitFor(t, sub, func(m PhaseMsg) bool { return m.Phase == want }, 2*time.Second)
		if want == PhaseSpinning {
			if got.Winning == nil {
				t.Fatal("Spinning phase missing Winning pocket")
			}
			if *got.Winning != roulette.Pocket(17) {
				t.Errorf("winning pocket want 17, got %s", got.Winning.Number())
			}
		}
	}
}

func TestPlaceBetDebitsWallet(t *testing.T) {
	w := newRecordingWallet()
	w.seed(42, 100)
	c := NewCoordinator(Config{
		Durations:     fastDurations,
		LastBetCutoff: 10 * time.Millisecond,
		Wallet:        w,
		Rng:           fixedRng{val: 7}, // pocket 7 is red
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	sub, unsub := c.Subscribe()
	defer unsub()
	<-sub // initial snapshot

	if err := c.PlaceBet(42, "alice", roulette.Bet{Key: roulette.BetKey{Type: roulette.BetRed}, Amount: 5}); err != nil {
		t.Fatalf("PlaceBet: %v", err)
	}
	if got := w.balance[42]; got != 95 {
		t.Errorf("balance after bet: want 95, got %d", got)
	}
	if len(w.debits) != 1 || w.debits[0].amount != 5 {
		t.Errorf("debit log: want [5], got %+v", w.debits)
	}

	// Aggregate broadcast should reflect the placed bet.
	msg := waitFor(t, sub, func(m PhaseMsg) bool { return m.Aggregate["red"] == 5 }, time.Second)
	if msg.Aggregate["red"] != 5 {
		t.Errorf("aggregate: want red=5, got %v", msg.Aggregate)
	}

	// Drive the cycle to Reveal; winner is 7 (red) so the bet pays 1:1 →
	// stake 5 + winnings 5 = gross 10 credited back.
	waitFor(t, sub, func(m PhaseMsg) bool { return m.Phase == PhaseReveal }, 2*time.Second)
	// Allow the credit goroutine inside settleRound to run.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		credited := len(w.credits)
		w.mu.Unlock()
		if credited == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.credits) != 1 {
		t.Fatalf("credit log: want 1 entry, got %v", w.credits)
	}
	if w.credits[0].amount != 10 {
		t.Errorf("credit amount: want 10 (stake+winnings), got %d", w.credits[0].amount)
	}
	if w.balance[42] != 105 {
		t.Errorf("balance after reveal: want 105, got %d", w.balance[42])
	}
}

func TestPlaceBetRejectedAfterCutoff(t *testing.T) {
	w := newRecordingWallet()
	w.seed(1, 100)
	// Long betting window + cutoff right at its end so we can land in the
	// "<= cutoff" path deterministically.
	c := NewCoordinator(Config{
		Durations: PhaseDurations{
			Betting:    150 * time.Millisecond,
			NoMoreBets: 30 * time.Millisecond,
			Spinning:   30 * time.Millisecond,
			Reveal:     30 * time.Millisecond,
		},
		LastBetCutoff: 200 * time.Millisecond, // larger than the betting window itself
		Wallet:        w,
		Rng:           fixedRng{val: 0},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	sub, unsub := c.Subscribe()
	defer unsub()
	<-sub

	err := c.PlaceBet(1, "alice", roulette.Bet{Key: roulette.BetKey{Type: roulette.BetRed}, Amount: 5})
	if err != ErrBettingClosed {
		t.Fatalf("PlaceBet: want ErrBettingClosed (cutoff exceeds remaining window), got %v", err)
	}
	if got := w.balance[1]; got != 100 {
		t.Errorf("balance: want 100 (no debit on rejected bet), got %d", got)
	}
}

func TestRevealCreditsAndLedgers(t *testing.T) {
	w := newRecordingWallet()
	w.seed(1, 100)
	w.seed(2, 100)
	led := &recordingLedger{}
	c := NewCoordinator(Config{
		Durations:     fastDurations,
		LastBetCutoff: 10 * time.Millisecond,
		Wallet:        w,
		Ledger:        led,
		Rng:           fixedRng{val: 7}, // 7 is red, in dozen1, column1
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	sub, unsub := c.Subscribe()
	defer unsub()
	<-sub

	// Alice bets 5 on RED (wins +5). Bob bets 10 on BLACK (loses).
	if err := c.PlaceBet(1, "alice", roulette.Bet{Key: roulette.BetKey{Type: roulette.BetRed}, Amount: 5}); err != nil {
		t.Fatalf("alice bet: %v", err)
	}
	if err := c.PlaceBet(2, "bob", roulette.Bet{Key: roulette.BetKey{Type: roulette.BetBlack}, Amount: 10}); err != nil {
		t.Fatalf("bob bet: %v", err)
	}

	waitFor(t, sub, func(m PhaseMsg) bool { return m.Phase == PhaseReveal }, 2*time.Second)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		led.mu.Lock()
		n := len(led.settlements)
		led.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	led.mu.Lock()
	defer led.mu.Unlock()
	if len(led.settlements) != 1 {
		t.Fatalf("ledger settlements: want 1, got %d", len(led.settlements))
	}
	s := led.settlements[0]
	if s.GameKey != "roulette" {
		t.Errorf("GameKey: want roulette, got %q", s.GameKey)
	}
	if len(s.Movements) != 2 {
		t.Fatalf("movements: want 2, got %d", len(s.Movements))
	}
	byUser := make(map[int64]gamesmp.PlayerMovement, 2)
	for _, m := range s.Movements {
		byUser[m.UserID] = m
	}
	if byUser[1].Wagered != 5 || byUser[1].Payout != 10 {
		t.Errorf("alice movement: want wagered=5 payout=10, got %+v", byUser[1])
	}
	if byUser[2].Wagered != 10 || byUser[2].Payout != 0 {
		t.Errorf("bob movement: want wagered=10 payout=0, got %+v", byUser[2])
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// Alice: -5 (debit) +10 (credit) = 105. Bob: -10 only = 90.
	if w.balance[1] != 105 {
		t.Errorf("alice balance: want 105, got %d", w.balance[1])
	}
	if w.balance[2] != 90 {
		t.Errorf("bob balance: want 90, got %d", w.balance[2])
	}
}

func TestHistoryRingPersistAndRestore(t *testing.T) {
	c := NewCoordinator(Config{
		Durations:     fastDurations,
		LastBetCutoff: 10 * time.Millisecond,
		Rng:           fixedRng{val: 17},
	})
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	sub, unsub := c.Subscribe()
	<-sub
	// Drive one full cycle so the history ring accumulates an entry.
	waitFor(t, sub, func(m PhaseMsg) bool { return m.Phase == PhaseReveal }, 2*time.Second)
	// One more so we have two entries to verify ordering.
	waitFor(t, sub, func(m PhaseMsg) bool { return m.Phase == PhaseBetting }, 2*time.Second)
	waitFor(t, sub, func(m PhaseMsg) bool { return m.Phase == PhaseReveal }, 2*time.Second)
	// PersistShape must be called while the actor is still running — main's
	// shutdown sequence pulls the shape, then cancels.
	shape := c.PersistShape()
	unsub()
	cancel()
	if len(shape.History) != 2 {
		t.Fatalf("persist history len: want 2, got %d", len(shape.History))
	}
	if shape.History[0] != roulette.Pocket(17) || shape.History[1] != roulette.Pocket(17) {
		t.Errorf("persist history items: want [17,17], got %v", shape.History)
	}

	// New coordinator should rehydrate the same history after Apply.
	c2 := NewCoordinator(Config{Durations: fastDurations, Rng: fixedRng{val: 0}})
	c2.ApplyPersistShape(shape)
	if got := c2.history.All(); len(got) != 2 {
		t.Errorf("restored history len: want 2, got %d", len(got))
	}
}

func TestDrainPendingRefunds(t *testing.T) {
	w := newRecordingWallet()
	w.seed(1, 100)
	c := NewCoordinator(Config{
		Durations: PhaseDurations{
			// Long betting window so we can place a bet then drain before
			// the cycle reaches Reveal.
			Betting:    5 * time.Second,
			NoMoreBets: 30 * time.Millisecond,
			Spinning:   30 * time.Millisecond,
			Reveal:     30 * time.Millisecond,
		},
		LastBetCutoff: 10 * time.Millisecond,
		Wallet:        w,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	sub, unsub := c.Subscribe()
	defer unsub()
	<-sub
	if err := c.PlaceBet(1, "alice", roulette.Bet{Key: roulette.BetKey{Type: roulette.BetRed}, Amount: 5}); err != nil {
		t.Fatalf("place bet: %v", err)
	}
	if w.balance[1] != 95 {
		t.Fatalf("post-bet balance: want 95, got %d", w.balance[1])
	}
	drained := c.DrainPending()
	if len(drained) != 1 || drained[0].Bet.Amount != 5 {
		t.Fatalf("drain: want one 5-credit bet, got %v", drained)
	}
}

func TestStatsRollup(t *testing.T) {
	r := historyRing{}
	for _, p := range []roulette.Pocket{1, 1, 3, roulette.Pocket(0), 2, 4} {
		// 1 (red), 1 (red), 3 (red), 0 (green), 2 (black), 4 (black)
		r.Push(p)
	}
	stats := r.ComputeStats()
	if stats.TotalSpins != 6 {
		t.Errorf("TotalSpins: want 6, got %d", stats.TotalSpins)
	}
	if stats.RedCount != 3 || stats.BlackCount != 2 || stats.GreenCount != 1 {
		t.Errorf("colors: want R=3 B=2 G=1, got R=%d B=%d G=%d", stats.RedCount, stats.BlackCount, stats.GreenCount)
	}
	if stats.LongestRed != 3 {
		t.Errorf("LongestRed: want 3, got %d", stats.LongestRed)
	}
	if stats.LongestBlack != 2 {
		t.Errorf("LongestBlack: want 2, got %d", stats.LongestBlack)
	}
	if len(stats.Hot) == 0 || stats.Hot[0].Pocket != 1 {
		t.Errorf("Hot[0]: want pocket 1, got %+v", stats.Hot)
	}
}
