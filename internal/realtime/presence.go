package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// PresenceService tracks who is currently connected, backed by Redis keys
// `presence:user:<handle>` with a short TTL refreshed by a per-session
// heartbeat. Mirrors the .NET PresenceService — the same TTL grace window
// (1 minute) lets a brief network blip not appear as a logout.
type PresenceService struct {
	Client     *redis.Client
	Logger     *slog.Logger
	HeartbeatTTL time.Duration // how long a single heartbeat keeps the user "online" (typical: 60s)
	HeartbeatEvery time.Duration // how often the per-session goroutine touches the key (typical: 30s)
}

// NewPresenceService builds a service with sensible defaults: 60s TTL,
// refresh every 30s so two heartbeats land inside one TTL window.
func NewPresenceService(client *redis.Client, logger *slog.Logger) *PresenceService {
	return &PresenceService{
		Client:         client,
		Logger:         logger,
		HeartbeatTTL:   60 * time.Second,
		HeartbeatEvery: 30 * time.Second,
	}
}

func (p *PresenceService) key(handle string) string {
	return "presence:user:" + strings.ToLower(handle)
}

// Heartbeat marks the user online and refreshes their TTL. Called once
// immediately on session start and then every HeartbeatEvery from the loop.
func (p *PresenceService) Heartbeat(ctx context.Context, handle string, userID int64) error {
	return p.Client.Set(ctx, p.key(handle), userID, p.HeartbeatTTL).Err()
}

// Clear immediately marks the user offline. Called on graceful session
// disconnect so the user isn't shown as online for the TTL grace window.
func (p *PresenceService) Clear(ctx context.Context, handle string) error {
	return p.Client.Del(ctx, p.key(handle)).Err()
}

// IsOnline returns true if the user has a live heartbeat key. Cheap — a
// single EXISTS roundtrip.
func (p *PresenceService) IsOnline(ctx context.Context, handle string) (bool, error) {
	n, err := p.Client.Exists(ctx, p.key(handle)).Result()
	if err != nil {
		return false, fmt.Errorf("presence: exists: %w", err)
	}
	return n > 0, nil
}

// OnlineMany batches an EXISTS for each handle in one MULTI/EXEC pipeline.
// Used by the chat sidebar to color N DM partners without N roundtrips.
// Empty input returns an empty map without touching Redis.
func (p *PresenceService) OnlineMany(ctx context.Context, handles []string) (map[string]bool, error) {
	if len(handles) == 0 {
		return map[string]bool{}, nil
	}
	pipe := p.Client.Pipeline()
	cmds := make(map[string]*redis.IntCmd, len(handles))
	for _, h := range handles {
		cmds[h] = pipe.Exists(ctx, p.key(h))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("presence: pipeline exec: %w", err)
	}
	out := make(map[string]bool, len(handles))
	for h, c := range cmds {
		n, _ := c.Result()
		out[h] = n > 0
	}
	return out, nil
}

// OnlineHandles enumerates everyone currently online by SCANning the keyspace
// for the presence prefix and returning the trailing handle of each key.
// Suitable for /who; not appropriate for per-render hot paths.
func (p *PresenceService) OnlineHandles(ctx context.Context) ([]string, error) {
	out := make([]string, 0, 16)
	var cursor uint64
	prefix := "presence:user:"
	for {
		keys, next, err := p.Client.Scan(ctx, cursor, prefix+"*", 64).Result()
		if err != nil {
			return nil, fmt.Errorf("presence: scan: %w", err)
		}
		for _, k := range keys {
			if h := strings.TrimPrefix(k, prefix); h != "" && h != k {
				out = append(out, h)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return out, nil
}

// RunHeartbeat is the session-lifetime loop: heartbeat immediately, then
// every HeartbeatEvery until ctx cancels, then Clear so the user falls off
// the online list without waiting for the TTL. Designed to be called as
//
//	go presence.RunHeartbeat(sess.Context(), handle, userID)
//
// from the transport layer.
func (p *PresenceService) RunHeartbeat(ctx context.Context, handle string, userID int64) {
	if err := p.Heartbeat(ctx, handle, userID); err != nil {
		p.Logger.Warn("presence: initial heartbeat", "handle", handle, "err", err)
	}
	ticker := time.NewTicker(p.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Clear with a separate ctx so the DEL still goes through even
			// though the session ctx is already canceled.
			clearCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := p.Clear(clearCtx, handle); err != nil {
				p.Logger.Warn("presence: clear on disconnect", "handle", handle, "err", err)
			}
			cancel()
			return
		case <-ticker.C:
			if err := p.Heartbeat(ctx, handle, userID); err != nil {
				p.Logger.Warn("presence: heartbeat tick", "handle", handle, "err", err)
			}
		}
	}
}
