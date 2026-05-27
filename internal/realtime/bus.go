// Package realtime is the Redis-backed pub/sub fabric for chat (and later:
// presence, read state, wall broadcasts).
package realtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// Bus is the minimal pub/sub contract every chat / presence / wall service
// depends on. Publish is fire-and-forget; Subscribe returns a channel of raw
// payload bytes that closes when the supplied context cancels.
type Bus interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Subscribe(ctx context.Context, topic string) (<-chan []byte, error)
}

// RedisBus is the production Bus implementation backed by go-redis/v9.
//
// Subscription model: one goroutine per topic per subscriber. Redis pub/sub
// is fanout-by-default, so we don't need a process-wide subscriber; the per-
// session subscription pattern keeps lifetimes tied to the SSH session ctx
// for clean teardown.
type RedisBus struct {
	client *redis.Client
	logger *slog.Logger
}

func NewRedisBus(client *redis.Client, logger *slog.Logger) *RedisBus {
	return &RedisBus{client: client, logger: logger}
}

func (b *RedisBus) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := b.client.Publish(ctx, topic, payload).Err(); err != nil {
		return fmt.Errorf("redis publish %q: %w", topic, err)
	}
	return nil
}

func (b *RedisBus) Subscribe(ctx context.Context, topic string) (<-chan []byte, error) {
	sub := b.client.Subscribe(ctx, topic)
	// Receive() blocks until the subscription is confirmed by the server, so
	// callers know that any Publish after Subscribe returns will be delivered.
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, fmt.Errorf("redis subscribe %q: %w", topic, err)
	}
	out := make(chan []byte, 32)
	go func() {
		defer close(out)
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- []byte(m.Payload):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// Topics is the shared catalogue of pub/sub topic names. Keeping them in one
// place makes drift between publisher and subscriber a compile error.
type Topics struct{}

var T Topics

// ChatChannel returns the pub/sub topic for a chat channel by ID.
func (Topics) ChatChannel(channelID int64) string {
	return fmt.Sprintf("chat:channel:%d", channelID)
}
