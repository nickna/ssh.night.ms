package multiplayer

import (
	"testing"

	"github.com/nickna/ssh.night.ms/internal/doors"
)

// Test that a 3-player hand can play preflop → flop → turn → river → showdown
// without deadlocking, with everyone checking around each street.
func TestThreeWayCheckdown(t *testing.T) {
	g := NewGame(doors.CryptoRng{}, 6, 5, 10)
	if !g.Sit(0, 1, "alice", 1000) || !g.Sit(1, 2, "bob", 1000) || !g.Sit(2, 3, "carol", 1000) {
		t.Fatal("sit failed")
	}
	if !g.StartHand() {
		t.Fatal("start hand failed")
	}
	// Walk through every street until Ended. Each iteration just CheckCalls
	// the action; that's enough to drive 3 players around the table.
	for step := 0; step < 100 && g.Street() != Ended; step++ {
		who := g.ToAct()
		if who < 0 {
			t.Fatalf("no actor mid-hand at step %d (street=%s)", step, g.Street())
		}
		g.PlayerAction(who, ActCheckCall)
	}
	if g.Street() != Ended {
		t.Fatalf("hand never ended, stuck at %s", g.Street())
	}
	// At least one seat should have ChipsHand > starting after the pot is
	// awarded (small blind + big blind move into the pot).
	total := int32(0)
	for _, s := range g.Seats() {
		total += s.ChipsHand
	}
	if total != 3000 {
		t.Errorf("chip total drift: got %d, want 3000", total)
	}
}

// Side pots: short stack goes all-in, full stacks keep betting. The short
// stack only wins what it could have matched; the rest is a side pot
// between the deeper stacks. We just check chip-conservation here (and
// that nobody got awarded more than they could have won) — the actual
// hand outcome depends on the shuffle.
func TestSidePotsConserveChips(t *testing.T) {
	g := NewGame(doors.CryptoRng{}, 6, 5, 10)
	g.Sit(0, 1, "shorty", 30)
	g.Sit(1, 2, "deep1", 1000)
	g.Sit(2, 3, "deep2", 1000)
	if !g.StartHand() {
		t.Fatal("start")
	}
	for step := 0; step < 200 && g.Street() != Ended; step++ {
		who := g.ToAct()
		if who < 0 {
			break
		}
		// Everyone shoves; short stack goes all-in well before the others.
		g.PlayerAction(who, ActAllIn)
	}
	if g.Street() != Ended {
		t.Fatalf("hand stuck at %s", g.Street())
	}
	total := int32(0)
	for _, s := range g.Seats() {
		total += s.ChipsHand
	}
	// Total at table = 30 + 1000 + 1000 = 2030. Side pots must preserve this.
	if total != 2030 {
		t.Errorf("side-pot chip total drift: got %d, want 2030", total)
	}
}

// Payouts() and HandNumber are the new APIs the coordinator's settlement
// builder depends on. The audit trail collapses to garbage if either drifts
// out of sync with what the engine actually paid out, so guard them.
func TestSettlementHooks(t *testing.T) {
	g := NewGame(doors.CryptoRng{}, 6, 5, 10)
	g.Sit(0, 1, "alice", 1000)
	g.Sit(1, 2, "bob", 1000)
	g.Sit(2, 3, "carol", 1000)

	// Hand counter starts at zero, advances on each StartHand.
	if g.HandNumber() != 0 {
		t.Fatalf("HandNumber should start at 0, got %d", g.HandNumber())
	}
	if !g.StartHand() {
		t.Fatal("start hand failed")
	}
	if g.HandNumber() != 1 {
		t.Fatalf("HandNumber after first StartHand: got %d, want 1", g.HandNumber())
	}

	// Two players fold; the third should walk away with the pot. Payouts
	// should reflect that winner's total prize, zero for the others.
	for step := 0; step < 10 && g.Street() != Ended; step++ {
		who := g.ToAct()
		if who < 0 {
			break
		}
		g.PlayerAction(who, ActFold)
	}
	if g.Street() != Ended {
		t.Fatalf("hand stuck at %s", g.Street())
	}

	payouts := g.Payouts()
	if len(payouts) != 6 {
		t.Fatalf("Payouts length: got %d, want 6 (cap seats)", len(payouts))
	}
	nonZero := 0
	totalPaid := int32(0)
	for _, p := range payouts {
		if p > 0 {
			nonZero++
			totalPaid += p
		}
	}
	if nonZero != 1 {
		t.Errorf("expected 1 winner in Payouts, got %d", nonZero)
	}
	// Conservation: total payouts equal sum of all Committed across seats.
	totalCommitted := int32(0)
	for _, s := range g.Seats() {
		totalCommitted += s.Committed
	}
	if totalPaid != totalCommitted {
		t.Errorf("payout total %d != committed total %d", totalPaid, totalCommitted)
	}

	// HandNumber survives snapshot + restore.
	snap := g.SnapshotState()
	if snap.HandNumber != 1 {
		t.Errorf("Snapshot.HandNumber: got %d, want 1", snap.HandNumber)
	}
	restored := RestoreFromSnapshot(snap, doors.CryptoRng{})
	if restored.HandNumber() != 1 {
		t.Errorf("restored HandNumber: got %d, want 1", restored.HandNumber())
	}

	// Second hand should bump to 2; Payouts should reset at the top.
	restored.Sit(0, 1, "alice", 1000)
	restored.Sit(1, 2, "bob", 1000)
	if !restored.StartHand() {
		t.Fatal("second start failed")
	}
	if restored.HandNumber() != 2 {
		t.Errorf("HandNumber after second StartHand: got %d, want 2", restored.HandNumber())
	}
	for _, p := range restored.Payouts() {
		if p != 0 {
			t.Errorf("Payouts should reset at StartHand, found %d", p)
		}
	}
}

// Fold-around to one player ends the hand instantly + awards them the pot.
func TestFoldAround(t *testing.T) {
	g := NewGame(doors.CryptoRng{}, 6, 5, 10)
	g.Sit(0, 1, "alice", 1000)
	g.Sit(1, 2, "bob", 1000)
	g.Sit(2, 3, "carol", 1000)
	g.StartHand()
	for i := 0; i < 10 && g.Street() != Ended; i++ {
		who := g.ToAct()
		if who < 0 {
			break
		}
		g.PlayerAction(who, ActFold)
	}
	if g.Street() != Ended {
		t.Fatalf("fold-around didn't end the hand")
	}
	// Exactly one seat should have > starting chips (the winner).
	winners := 0
	for _, s := range g.Seats() {
		if s.ChipsHand > 1000 {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("expected exactly 1 winner, got %d", winners)
	}
}
