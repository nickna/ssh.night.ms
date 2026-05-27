package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// WallTopic is the Redis pub/sub topic carrying sysop wall broadcasts. One
// publisher (Sysop screen) → one process-wide subscriber per node → fan-out
// to every active session's local channel.
const WallTopic = "system:wall"

// WallMessage is the on-the-wire DTO. Field names match the legacy stack's
// wire shape so a legacy subscriber (if any were still running mid-cutover)
// could decode rows we publish. Cutover is big-bang in practice, but keeping
// the wire compatible is cheap.
type WallMessage struct {
	From       string    `json:"from"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

// WallDispatcher is a process-singleton that subscribes once to system:wall
// and fans broadcasts out to every per-session subscriber. Sessions never
// touch the Bus directly for wall — they go through Subscribe() here so we
// pay one deserialize per broadcast instead of N (one per session).
type WallDispatcher struct {
	bus    Bus
	logger *slog.Logger

	mu   sync.Mutex
	subs map[int64]*wallSub
	next int64
}

// wallSub bundles a subscriber's delivery channel with the running count of
// consecutive dropped messages. A chronically slow consumer (network stall,
// stuck render loop) is evicted after evictAfterDrops to free its goroutine
// and force a fresh subscription on reconnect.
type wallSub struct {
	ch    chan WallMessage
	drops int
}

// evictAfterDrops caps consecutive drops before we close out a subscriber.
// 4 broadcasts in a row missed is well past "transient blip" territory.
const evictAfterDrops = 4

// wallSubBufferSize is the per-subscriber channel capacity. Sized to absorb
// reasonable bursts (multiple sysop notices in quick succession) without
// drops on healthy consumers.
const wallSubBufferSize = 16

// NewWallDispatcher builds the dispatcher. Run() starts the subscription
// loop; cancel its ctx on shutdown.
func NewWallDispatcher(bus Bus, logger *slog.Logger) *WallDispatcher {
	return &WallDispatcher{
		bus:    bus,
		logger: logger,
		subs:   make(map[int64]*wallSub),
	}
}

// Run is the long-lived subscription loop. main spawns it as a goroutine
// under the root errgroup so it stops when the server shuts down. Transient
// Redis errors retry with a small backoff — losing the loop would silently
// drop every future broadcast.
func (d *WallDispatcher) Run(ctx context.Context) error {
	backoff := 500 * time.Millisecond
	const maxBackoff = 30 * time.Second
	for {
		if err := d.consume(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			d.logger.Warn("wall subscribe lost; retrying", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		return nil
	}
}

func (d *WallDispatcher) consume(ctx context.Context) error {
	ch, err := d.bus.Subscribe(ctx, WallTopic)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case payload, ok := <-ch:
			if !ok {
				return errors.New("wall channel closed")
			}
			var msg WallMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				d.logger.Warn("wall: bad payload", "err", err)
				continue
			}
			d.logger.Debug("wall fanout", "from", msg.From, "subscribers", d.subscriberCount())
			d.fanout(msg)
		}
	}
}

func (d *WallDispatcher) subscriberCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.subs)
}

// fanout pushes the broadcast to every registered subscriber's channel. Uses
// a non-blocking select so one stuck session can't gum up the rest — the
// buffered channel absorbs the bursty case. A subscriber that drops
// evictAfterDrops broadcasts in a row is unsubscribed and its channel closed,
// which signals the per-session listener to tear down and reconnect on its
// next interaction. We hold the write lock (not RLock) because we mutate
// per-sub drop counters and may evict; the iteration is bounded and the
// critical section is short, so contention is negligible.
func (d *WallDispatcher) fanout(msg WallMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, sub := range d.subs {
		select {
		case sub.ch <- msg:
			sub.drops = 0
		default:
			sub.drops++
			d.logger.Warn("wall: subscriber buffer full; dropped",
				"sub_id", id, "consecutive_drops", sub.drops)
			if sub.drops >= evictAfterDrops {
				d.logger.Warn("wall: evicting chronically slow subscriber",
					"sub_id", id, "drops", sub.drops)
				close(sub.ch)
				delete(d.subs, id)
			}
		}
	}
}

// Subscribe registers a per-session receiver. Returns the channel (read-only
// from the caller's perspective) and an unregister func — call it from the
// session's teardown to free the slot and let GC reclaim the channel. The
// channel may be closed by the dispatcher itself if this subscriber is
// chronically slow; callers should already handle channel close as the
// signal to stop listening.
func (d *WallDispatcher) Subscribe() (<-chan WallMessage, func()) {
	sub := &wallSub{ch: make(chan WallMessage, wallSubBufferSize)}
	d.mu.Lock()
	d.next++
	id := d.next
	d.subs[id] = sub
	d.mu.Unlock()
	cancel := func() {
		d.mu.Lock()
		if existing, ok := d.subs[id]; ok && existing == sub {
			delete(d.subs, id)
		}
		d.mu.Unlock()
	}
	return sub.ch, cancel
}

// Publish posts a wall message to Redis. The dispatcher's own Run loop will
// receive its own publication and fan it out — that's by design, so the
// originating session sees the broadcast like everyone else (and would see
// it via a second process node too, if we had one).
func (d *WallDispatcher) Publish(ctx context.Context, from, message string) error {
	body, err := json.Marshal(WallMessage{
		From:       from,
		Message:    message,
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	d.logger.Info("wall: publish", "from", from, "len", len(message))
	return d.bus.Publish(ctx, WallTopic, body)
}
