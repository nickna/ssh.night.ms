package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeQueries records every delete call so tests can assert what the sweeper
// issued. Per-severity row counts are configurable; bans default to 0.
type fakeQueries struct {
	mu sync.Mutex

	// deleteCalls records (severity, cutoff) for each event prune, in order.
	deleteCalls []eventDelete
	bansCalls   int

	// rowsBySeverity controls the int64 the event delete returns for a
	// severity. Missing key → 0 rows.
	rowsBySeverity map[string]int64
	bansRows       int64

	// errOnSeverity, when set, makes the matching event delete return an error.
	errOnSeverity string
	errBans       bool
}

type eventDelete struct {
	severity string
	cutoff   time.Time
}

func (f *fakeQueries) DeleteSecurityEventsBySeverityOlderThan(_ context.Context, arg gen.DeleteSecurityEventsBySeverityOlderThanParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, eventDelete{severity: arg.Severity, cutoff: arg.At.Time})
	if f.errOnSeverity != "" && arg.Severity == f.errOnSeverity {
		return 0, errors.New("boom")
	}
	return f.rowsBySeverity[arg.Severity], nil
}

func (f *fakeQueries) DeleteExpiredIPBans(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bansCalls++
	if f.errBans {
		return 0, errors.New("ban boom")
	}
	return f.bansRows, nil
}

func (f *fakeQueries) calls() []eventDelete {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]eventDelete, len(f.deleteCalls))
	copy(out, f.deleteCalls)
	return out
}

// TestSweepCutoffsAndSeverities verifies that one sweep issues the expected
// severity tiers with cutoffs computed as now-TTL, and always reaps bans.
func TestSweepCutoffsAndSeverities(t *testing.T) {
	fq := &fakeQueries{}
	infoTTL := 30 * 24 * time.Hour
	warnTTL := 365 * 24 * time.Hour
	s := New(fq, quietLogger(), time.Hour, infoTTL, warnTTL)

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := s.sweep(context.Background(), now); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	calls := fq.calls()
	want := []eventDelete{
		{severity: "info", cutoff: now.Add(-infoTTL)},
		{severity: "warn", cutoff: now.Add(-warnTTL)},
		{severity: "crit", cutoff: now.Add(-warnTTL)},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d event deletes, want %d: %+v", len(calls), len(want), calls)
	}
	for i, w := range want {
		if calls[i].severity != w.severity || !calls[i].cutoff.Equal(w.cutoff) {
			t.Errorf("call %d = {%s, %s}, want {%s, %s}", i,
				calls[i].severity, calls[i].cutoff, w.severity, w.cutoff)
		}
	}
	if fq.bansCalls != 1 {
		t.Errorf("DeleteExpiredIPBans called %d times, want 1", fq.bansCalls)
	}
}

// TestSweepZeroTTLDisablesTier verifies that a zero TTL skips that tier's
// deletes while still reaping bans.
func TestSweepZeroTTLDisablesTier(t *testing.T) {
	t.Run("info disabled", func(t *testing.T) {
		fq := &fakeQueries{}
		s := New(fq, quietLogger(), time.Hour, 0, 365*24*time.Hour)
		if err := s.sweep(context.Background(), time.Now()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		for _, c := range fq.calls() {
			if c.severity == "info" {
				t.Errorf("info tier pruned despite zero TTL")
			}
		}
		if got := len(fq.calls()); got != 2 { // warn + crit only
			t.Errorf("got %d event deletes, want 2", got)
		}
		if fq.bansCalls != 1 {
			t.Errorf("bans reaped %d times, want 1", fq.bansCalls)
		}
	})

	t.Run("both disabled still reaps bans", func(t *testing.T) {
		fq := &fakeQueries{}
		s := New(fq, quietLogger(), time.Hour, 0, 0)
		if err := s.sweep(context.Background(), time.Now()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := len(fq.calls()); got != 0 {
			t.Errorf("got %d event deletes, want 0", got)
		}
		if fq.bansCalls != 1 {
			t.Errorf("bans reaped %d times, want 1", fq.bansCalls)
		}
	})
}

// TestSweepCounterIncrements verifies pruned rows fold into the expvar counter.
func TestSweepCounterIncrements(t *testing.T) {
	fq := &fakeQueries{
		rowsBySeverity: map[string]int64{"info": 10, "warn": 3, "crit": 2},
	}
	s := New(fq, quietLogger(), time.Hour, 24*time.Hour, 24*time.Hour)

	before := securityEventsPrunedCounter.Value()
	if err := s.sweep(context.Background(), time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := securityEventsPrunedCounter.Value() - before; got != 15 {
		t.Errorf("counter delta = %d, want 15", got)
	}
}

// TestSweepErrorShortCircuits verifies a delete error aborts the round and
// propagates, so the loop can log-and-retry.
func TestSweepErrorShortCircuits(t *testing.T) {
	fq := &fakeQueries{errOnSeverity: "info"}
	s := New(fq, quietLogger(), time.Hour, 24*time.Hour, 24*time.Hour)
	if err := s.sweep(context.Background(), time.Now()); err == nil {
		t.Fatal("expected error, got nil")
	}
	// Bans should not have been reached after the info error.
	if fq.bansCalls != 0 {
		t.Errorf("bans reaped despite earlier error")
	}
}

// TestRunSweepsThenExitsOnCancel verifies Run does an initial sweep and exits
// promptly when the context is cancelled. Mirrors wall_test's run-loop shape.
func TestRunSweepsThenExitsOnCancel(t *testing.T) {
	fq := &fakeQueries{}
	// Long interval so the only sweep we observe is the initial one.
	s := New(fq, quietLogger(), time.Hour, 24*time.Hour, 24*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(runDone)
	}()

	// The initial sweep runs synchronously at the top of Run, so the ban reap
	// should register quickly.
	deadline := time.After(time.Second)
	for {
		fq.mu.Lock()
		n := fq.bansCalls
		fq.mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("initial sweep did not run")
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}

// TestNewDefaultsInterval verifies a non-positive interval falls back.
func TestNewDefaultsInterval(t *testing.T) {
	s := New(&fakeQueries{}, quietLogger(), 0, time.Hour, time.Hour)
	if s.interval != defaultInterval {
		t.Errorf("interval = %s, want %s", s.interval, defaultInterval)
	}
}
