package realtime

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// quietLogger discards every log line so test output stays focused on
// assertion failures. Use this everywhere a *slog.Logger is needed only for
// the consumer to call Warn/Info without panic.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWallDispatcherFanout(t *testing.T) {
	bus := newFakeBus()
	d := NewWallDispatcher(bus, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(runDone)
	}()
	// Give Run() a moment to land its Subscribe before we Publish.
	waitFor(t, func() bool { return bus.SubscriberCount(WallTopic) == 1 })

	ch1, cancel1 := d.Subscribe()
	defer cancel1()
	ch2, cancel2 := d.Subscribe()
	defer cancel2()

	if err := d.Publish(ctx, "alice", "hello, world"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got1 := receive(t, ch1, time.Second)
	got2 := receive(t, ch2, time.Second)
	if got1.From != "alice" || got1.Message != "hello, world" {
		t.Errorf("sub1 = %+v, want {alice, hello, world}", got1)
	}
	if got2.From != "alice" || got2.Message != "hello, world" {
		t.Errorf("sub2 = %+v, want {alice, hello, world}", got2)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}

func TestWallDispatcherUnsubscribeRemovesSlot(t *testing.T) {
	bus := newFakeBus()
	d := NewWallDispatcher(bus, quietLogger())

	_, cancel1 := d.Subscribe()
	_, cancel2 := d.Subscribe()
	if got := d.subscriberCount(); got != 2 {
		t.Fatalf("after two subs: count = %d, want 2", got)
	}
	cancel1()
	if got := d.subscriberCount(); got != 1 {
		t.Fatalf("after cancel1: count = %d, want 1", got)
	}
	cancel2()
	if got := d.subscriberCount(); got != 0 {
		t.Fatalf("after cancel2: count = %d, want 0", got)
	}
	// Idempotent: calling cancel twice should not panic or underflow.
	cancel1()
	if got := d.subscriberCount(); got != 0 {
		t.Errorf("after double cancel: count = %d, want 0", got)
	}
}

func TestWallDispatcherDropsFullSubscriber(t *testing.T) {
	bus := newFakeBus()
	d := NewWallDispatcher(bus, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitFor(t, func() bool { return bus.SubscriberCount(WallTopic) == 1 })

	// Subscribe but don't drain — the chan buffer is 4. Publishing 10 in a row
	// must not block; the dispatcher should drop the overflow rather than
	// stall the loop.
	ch, unsub := d.Subscribe()
	defer unsub()

	for i := 0; i < 10; i++ {
		if err := d.Publish(ctx, "alice", "spam"); err != nil {
			t.Fatalf("Publish #%d: %v", i, err)
		}
	}
	// Drain whatever made it through; we just care the dispatcher didn't lock up.
	deadline := time.After(200 * time.Millisecond)
	count := 0
drain:
	for {
		select {
		case <-ch:
			count++
		case <-deadline:
			break drain
		}
	}
	if count == 0 {
		t.Errorf("expected at least one delivered message, got 0")
	}
	if count > 10 {
		t.Errorf("got %d messages, expected <= 10 (no duplication)", count)
	}
}

func TestWallDispatcherBadPayloadSkipsButContinues(t *testing.T) {
	bus := newFakeBus()
	d := NewWallDispatcher(bus, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitFor(t, func() bool { return bus.SubscriberCount(WallTopic) == 1 })

	ch, unsub := d.Subscribe()
	defer unsub()

	// Garbage payload first; loop should log + skip rather than die.
	if err := bus.Publish(ctx, WallTopic, []byte("{not json")); err != nil {
		t.Fatalf("publish bad: %v", err)
	}
	// Then a valid one — the subscriber should see it.
	if err := d.Publish(ctx, "alice", "still alive"); err != nil {
		t.Fatalf("publish good: %v", err)
	}
	got := receive(t, ch, time.Second)
	if got.From != "alice" {
		t.Errorf("after bad payload, valid one not delivered: %+v", got)
	}
}

// waitFor polls cond every 5ms up to 1s before failing the test. Used so
// tests don't race on goroutine startup ordering.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("wait condition never became true within 1s")
}

func receive(t *testing.T, ch <-chan WallMessage, timeout time.Duration) WallMessage {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for wall message after %s", timeout)
		return WallMessage{}
	}
}
