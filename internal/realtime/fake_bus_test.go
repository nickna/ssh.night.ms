package realtime

import (
	"context"
	"sync"
)

// fakeBus is an in-process pub/sub used by tests to exercise the chat / wall
// dispatch paths without a live Redis. Topic semantics match the production
// Bus contract enough that the wired-up consumers (WallDispatcher.Run, the
// ChatService Subscribe* methods) don't notice the swap.
//
// Subscribers receive every Publish after their Subscribe call. Subscribes
// before the first Publish on a topic are fine — the goroutine just blocks.
// Closing a subscriber's ctx tears down its delivery goroutine cleanly.
type fakeBus struct {
	mu     sync.Mutex
	topics map[string][]chan []byte // one chan per active subscriber
}

func newFakeBus() *fakeBus {
	return &fakeBus{topics: map[string][]chan []byte{}}
}

func (b *fakeBus) Publish(ctx context.Context, topic string, payload []byte) error {
	b.mu.Lock()
	subs := append([]chan []byte(nil), b.topics[topic]...) // snapshot
	b.mu.Unlock()
	for _, ch := range subs {
		// Non-blocking like real Redis pub/sub (a slow subscriber gets a
		// dropped message rather than backpressuring the publisher).
		select {
		case ch <- payload:
		default:
		}
	}
	return nil
}

func (b *fakeBus) Subscribe(ctx context.Context, topic string) (<-chan []byte, error) {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.topics[topic] = append(b.topics[topic], ch)
	b.mu.Unlock()

	out := make(chan []byte, 16)
	go func() {
		defer close(out)
		defer b.unsubscribe(topic, ch)
		for {
			select {
			case <-ctx.Done():
				return
			case p, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- p:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (b *fakeBus) unsubscribe(topic string, target chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.topics[topic]
	out := subs[:0]
	for _, c := range subs {
		if c != target {
			out = append(out, c)
		}
	}
	b.topics[topic] = out
}

// SubscriberCount exposes the internal subscriber list size for tests that
// want to assert teardown actually freed the slot.
func (b *fakeBus) SubscriberCount(topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.topics[topic])
}

// Compile-time guard so fakeBus drifting out of the Bus contract is a build
// error, not a runtime surprise.
var _ Bus = (*fakeBus)(nil)
