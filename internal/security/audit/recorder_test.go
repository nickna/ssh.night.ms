package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// recorderForTest builds a PostgresRecorder with a nil *gen.Queries — fine
// because we never call Run() in tests that only exercise Record(); the
// buffer fills up locally and we read it back. Any test that calls Run
// must pass real queries (we have none in unit-test scope here).
func recorderForTest(bufSize int, logger *slog.Logger) *PostgresRecorder {
	return NewPostgresRecorder(nil, logger, bufSize)
}

func TestRecorder_SlogAlwaysEmits_PassThroughBuffer(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := recorderForTest(10, logger)

	r.Record(context.Background(), AuthFailure{
		Handle: "alice", IP: "203.0.113.1", Method: "password", Reason: "invalid password",
	})

	if !strings.Contains(buf.String(), `"event_type":"auth_failure"`) {
		t.Fatalf("slog line missing event_type; got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"handle":"alice"`) {
		t.Fatalf("slog line missing handle; got: %s", buf.String())
	}
}

func TestRecorder_SeverityRoutesToCorrectSlogLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := recorderForTest(10, logger)

	r.Record(context.Background(), AuthSuccess{Handle: "ok", IP: "1.1.1.1", Method: "password"})
	r.Record(context.Background(), LockoutIP{IP: "2.2.2.2", Fails: 20, Lockcount: 2, Duration: 30 * time.Minute})
	r.Record(context.Background(), PersistentBanAuto{IP: "3.3.3.3", Lockcount: 3, ExpiresAt: time.Now().Add(24 * time.Hour)})

	out := buf.String()
	if !strings.Contains(out, `"level":"INFO"`) {
		t.Errorf("expected INFO line for AuthSuccess; got: %s", out)
	}
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("expected WARN line for LockoutIP; got: %s", out)
	}
	if !strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("expected ERROR line for PersistentBanAuto; got: %s", out)
	}
}

func TestRecorder_BufferFullDropsAndIncrementsCounter(t *testing.T) {
	startVal := auditDroppedCounter.Value()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := recorderForTest(2, logger) // tiny buffer

	// Push more than capacity. None get drained (no Run goroutine).
	for i := 0; i < 5; i++ {
		r.Record(context.Background(), AuthFailure{Handle: "x", IP: "1.1.1.1", Method: "password", Reason: "test"})
	}

	dropped := auditDroppedCounter.Value() - startVal
	if dropped < 3 {
		t.Fatalf("expected at least 3 drops (5 sent, capacity 2); got %d", dropped)
	}
	// Slog ran for every event including the dropped ones. Count the
	// "security event" message specifically — `event_type` also appears as
	// an attribute on the rate-limited "audit buffer full" warn line, so
	// a naive substring match would over-count.
	gotLines := strings.Count(buf.String(), `"msg":"security event"`)
	if gotLines != 5 {
		t.Errorf("expected slog line for every event (incl. drops); got %d for 5 calls", gotLines)
	}
}

func TestRecorder_DropWarnRateLimited(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := recorderForTest(1, logger)

	// Saturate quickly.
	for i := 0; i < 20; i++ {
		r.Record(context.Background(), AuthFailure{Handle: "x", IP: "1.1.1.1", Method: "password", Reason: "test"})
	}
	// The "audit buffer full" warn should fire at most once for this burst
	// (rate-limited to 1/sec).
	bursts := strings.Count(buf.String(), "audit buffer full")
	if bursts > 1 {
		t.Errorf("buffer-full warn should rate-limit to 1; got %d", bursts)
	}
}

func TestRecorder_RunStopsOnContextCancel(t *testing.T) {
	r := recorderForTest(10, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit on context cancel")
	}
}

func TestNoopRecorder_DoesNotPanic(t *testing.T) {
	NoopRecorder{}.Record(context.Background(), nil)
	NoopRecorder{}.Record(context.Background(), AuthSuccess{Handle: "x"})
}

// TestEvent_AllSatisfyInterface is a compile-time-flavored check that every
// concrete event in events.go implements the Event interface fully — if a
// new event forgets a method, this fails to compile.
func TestEvent_AllSatisfyInterface(t *testing.T) {
	var _ Event = AuthSuccess{}
	var _ Event = AuthFailure{}
	var _ Event = LockoutHandle{}
	var _ Event = LockoutIP{}
	var _ Event = PersistentBanAuto{}
	var _ Event = PersistentBanManual{}
	var _ Event = PersistentBanRevoke{}
	var _ Event = ConnRejectedOverlimit{}
	var _ Event = HandshakeFailed{}
}

// Ensure the counter is process-global (not per-recorder) — multiple
// recorder instances increment the same counter so operators see the
// aggregate drop rate via /debug/vars.
func TestRecorder_DroppedCounterIsProcessWide(t *testing.T) {
	startVal := auditDroppedCounter.Value()
	r1 := recorderForTest(1, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	r2 := recorderForTest(1, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	for i := 0; i < 5; i++ {
		r1.Record(context.Background(), HandshakeFailed{IP: "1.1.1.1", Err: "x"})
		r2.Record(context.Background(), HandshakeFailed{IP: "2.2.2.2", Err: "x"})
	}
	if delta := auditDroppedCounter.Value() - startVal; delta < 6 {
		t.Errorf("expected drops from both recorders to accumulate; delta=%d", delta)
	}
}
