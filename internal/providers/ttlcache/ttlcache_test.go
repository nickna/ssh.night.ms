package ttlcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFreshHitSkipsFetch(t *testing.T) {
	c := New[string, int](time.Hour, nil)
	ctx := context.Background()

	var calls int32
	fetch := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 42, nil
	}
	for i := 0; i < 5; i++ {
		v, err := c.Get(ctx, "k", fetch)
		if err != nil || v != 42 {
			t.Fatalf("get #%d: got (%d, %v), want (42, nil)", i, v, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fetch called %d times, want 1 (cache should serve subsequent hits)", got)
	}
}

func TestExpiredEntryRefetches(t *testing.T) {
	c := New[string, int](10*time.Millisecond, nil)
	ctx := context.Background()

	var calls int32
	fetch := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return int(atomic.LoadInt32(&calls)), nil
	}
	if v, _ := c.Get(ctx, "k", fetch); v != 1 {
		t.Fatalf("first get = %d, want 1", v)
	}
	time.Sleep(20 * time.Millisecond)
	if v, _ := c.Get(ctx, "k", fetch); v != 2 {
		t.Fatalf("post-expiry get = %d, want 2 (should refetch)", v)
	}
}

func TestZeroTTLNeverExpires(t *testing.T) {
	c := New[string, int](0, nil)
	ctx := context.Background()

	var calls int32
	fetch := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 7, nil
	}
	_, _ = c.Get(ctx, "k", fetch)
	time.Sleep(30 * time.Millisecond)
	v, _ := c.Get(ctx, "k", fetch)
	if v != 7 {
		t.Errorf("value = %d, want 7", v)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (ttl=0 means cache forever)", got)
	}
}

func TestSingleflightCoalescesConcurrentGets(t *testing.T) {
	c := New[string, int](time.Hour, nil)
	ctx := context.Background()

	var calls int32
	gate := make(chan struct{})
	fetch := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		<-gate
		return 99, nil
	}

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			v, err := c.Get(ctx, "k", fetch)
			if err != nil || v != 99 {
				t.Errorf("concurrent get = (%d, %v), want (99, nil)", v, err)
			}
		}()
	}
	// Let all goroutines stack up on the inflight call before releasing.
	time.Sleep(10 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream called %d times, want 1 (singleflight should coalesce)", got)
	}
}

func TestStaleOnErrorReturnsLastValue(t *testing.T) {
	c := New[string, int](10*time.Millisecond, nil, StaleOnError())
	ctx := context.Background()

	var calls int32
	fetch := func(ctx context.Context) (int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return 5, nil
		}
		return 0, errors.New("upstream down")
	}
	if v, err := c.Get(ctx, "k", fetch); v != 5 || err != nil {
		t.Fatalf("first get = (%d, %v), want (5, nil)", v, err)
	}
	time.Sleep(20 * time.Millisecond)
	v, err := c.Get(ctx, "k", fetch)
	if err != nil {
		t.Fatalf("stale-on-error path returned err = %v, want nil", err)
	}
	if v != 5 {
		t.Errorf("got %d, want 5 (stale value)", v)
	}
}

func TestNoStaleFlagPropagatesError(t *testing.T) {
	c := New[string, int](10*time.Millisecond, nil)
	ctx := context.Background()

	var calls int32
	fetch := func(ctx context.Context) (int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return 5, nil
		}
		return 0, errors.New("upstream down")
	}
	_, _ = c.Get(ctx, "k", fetch)
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Get(ctx, "k", fetch); err == nil {
		t.Errorf("expected error to propagate without StaleOnError")
	}
}

func TestInvalidateForcesRefetch(t *testing.T) {
	c := New[string, int](time.Hour, nil)
	ctx := context.Background()

	var calls int32
	fetch := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return int(atomic.LoadInt32(&calls)), nil
	}
	_, _ = c.Get(ctx, "k", fetch)
	c.Invalidate("k")
	v, _ := c.Get(ctx, "k", fetch)
	if v != 2 {
		t.Errorf("post-invalidate get = %d, want 2", v)
	}
}

func TestPeekDoesNotFetch(t *testing.T) {
	c := New[string, int](time.Hour, nil)
	if _, ok := c.Peek("missing"); ok {
		t.Errorf("Peek on missing key returned ok=true")
	}
	_, _ = c.Get(context.Background(), "k", func(context.Context) (int, error) { return 12, nil })
	v, ok := c.Peek("k")
	if !ok || v != 12 {
		t.Errorf("Peek after Get = (%d, %v), want (12, true)", v, ok)
	}
}
