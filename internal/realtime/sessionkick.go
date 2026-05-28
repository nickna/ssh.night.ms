package realtime

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// SessionKickTopic carries cross-node "drop every session for user N" events.
// Payload is the decimal user id; subscribers walk their local registry and
// invoke each close func for the matching userID. Modeled on the
// security:ban-invalidate channel — single producer, every node subscribes.
const SessionKickTopic = "session:kick"

// SessionKicker is the process-singleton that lets the sysop console boot a
// user off both the SSH and WebSocket entry points. Each session registers a
// close func at construction time (calling Close on the wish.Session or the
// websocket.Conn); Kick walks the local registry, invokes the matching close
// funcs, and broadcasts on Redis so peer replicas drop their sessions too.
type SessionKicker struct {
	bus    Bus
	logger *slog.Logger

	mu    sync.Mutex
	next  int64
	regs  map[int64][]registration // userID -> registrations
}

type registration struct {
	id    int64
	close func()
}

// NewSessionKicker constructs a kicker. Spawn Run as a goroutine for the
// cross-node subscription side.
func NewSessionKicker(bus Bus, logger *slog.Logger) *SessionKicker {
	return &SessionKicker{
		bus:    bus,
		logger: logger,
		regs:   make(map[int64][]registration),
	}
}

// Register stores a close func to be invoked when this user is kicked.
// Returns a deregister func the caller MUST defer on session teardown —
// otherwise the entry leaks and a later Kick would call into a freed
// session.
//
// close MUST be idempotent — Kick may invoke it via both the local-fanout
// path and the Redis-roundtrip path on the same node.
func (k *SessionKicker) Register(userID int64, close func()) func() {
	k.mu.Lock()
	k.next++
	id := k.next
	k.regs[userID] = append(k.regs[userID], registration{id: id, close: close})
	k.mu.Unlock()

	return func() {
		k.mu.Lock()
		list := k.regs[userID]
		for i := range list {
			if list[i].id == id {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(k.regs, userID)
		} else {
			k.regs[userID] = list
		}
		k.mu.Unlock()
	}
}

// Kick closes every local session for userID and publishes to Redis so
// peer nodes do the same. Safe on offline users (no local matches, the
// publish is still a no-op on the receiver side).
func (k *SessionKicker) Kick(ctx context.Context, userID int64) error {
	k.kickLocal(userID)
	if k.bus == nil {
		return nil
	}
	return k.bus.Publish(ctx, SessionKickTopic, []byte(strconv.FormatInt(userID, 10)))
}

// kickLocal walks the registry for userID and invokes every close func.
// The close funcs run inside the lock — they MUST be cheap (a single
// Close() on a network primitive is fine; anything heavier should
// dispatch a goroutine).
func (k *SessionKicker) kickLocal(userID int64) int {
	k.mu.Lock()
	list := k.regs[userID]
	k.mu.Unlock()
	for _, r := range list {
		r.close()
	}
	return len(list)
}

// Run is the long-lived subscription loop. Spawn from main.go alongside
// the other realtime services; cancel the parent ctx to shut down.
// Transient Redis errors retry with exponential backoff so a brief
// outage doesn't permanently stop cross-node kicks.
func (k *SessionKicker) Run(ctx context.Context) error {
	if k.bus == nil {
		return nil
	}
	backoff := 500 * time.Millisecond
	const maxBackoff = 30 * time.Second
	for {
		if err := k.consume(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			k.logger.Warn("session-kick subscribe lost; retrying", "err", err, "backoff", backoff)
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

func (k *SessionKicker) consume(ctx context.Context) error {
	ch, err := k.bus.Subscribe(ctx, SessionKickTopic)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case payload, ok := <-ch:
			if !ok {
				return errors.New("session-kick channel closed")
			}
			userID, perr := strconv.ParseInt(string(payload), 10, 64)
			if perr != nil {
				k.logger.Warn("session-kick: bad payload", "payload", string(payload), "err", perr)
				continue
			}
			closed := k.kickLocal(userID)
			k.logger.Debug("session-kick: received", "user_id", userID, "local_closed", closed)
		}
	}
}
