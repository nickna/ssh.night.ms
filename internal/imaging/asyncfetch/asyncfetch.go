// Package asyncfetch is the shared image-download scheduler used by every
// screen that paints inline images (chat, browser, …). It collapses the
// duplicated "buffered-slot channel + sync.Mutex map + pending tracker"
// pattern those screens used to maintain into one process-wide Pool.
//
// Responsibilities:
//   - Cap total in-flight HTTP fetches (network-friendly: a hostile paste
//     of a thousand image URLs can't open a thousand sockets).
//   - Apply a per-fetch timeout so a slow upstream can't pin a slot forever.
//   - Decode the image into image.Image so each screen can render at its
//     own width / protocol locally without re-doing the network IO.
//
// Caching of the *rendered* output (which is the per-screen, per-width
// concern) lives in the consuming screen via ttlcache.Cache — this package
// stays render-agnostic.
package asyncfetch

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"time"

	"github.com/nickna/ssh.night.ms/internal/imaging"
)

// ErrSlotTimeout is returned when a fetch could not acquire a concurrency
// slot before the SlotTimeout elapsed. Surfaced as a soft failure so the
// caller can render a placeholder rather than block the tea loop.
var ErrSlotTimeout = errors.New("asyncfetch: slot acquire timeout")

// Pool serializes inline-image fetches across the process. One instance lives
// on session.Deps so every screen shares the same network budget — a chat
// paste-storm doesn't starve the browser screen and vice versa.
//
// Pool itself is render-agnostic: it returns image.Image. Screens render to
// their own halfblock / Kitty / iTerm output at whatever width they need.
type Pool struct {
	sem          chan struct{}
	fetcher      *imaging.Fetcher
	fetchTimeout time.Duration
	slotTimeout  time.Duration
	logger       *slog.Logger
}

// NewPool builds a Pool with a buffered slot channel of size maxInFlight.
// fetchTimeout caps each individual upstream HTTP+decode call.
// slotTimeout caps how long a queued fetch waits for a slot to free up.
func NewPool(maxInFlight int, fetchTimeout, slotTimeout time.Duration, logger *slog.Logger) *Pool {
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	if fetchTimeout <= 0 {
		fetchTimeout = 6 * time.Second
	}
	if slotTimeout <= 0 {
		slotTimeout = 20 * time.Second
	}
	return &Pool{
		sem:          make(chan struct{}, maxInFlight),
		fetcher:      imaging.New(),
		fetchTimeout: fetchTimeout,
		slotTimeout:  slotTimeout,
		logger:       logger,
	}
}

// Fetch blocks on a slot, then downloads + decodes the URL. ctx propagates
// into the slot wait and the fetch — cancelling ctx unblocks both. The
// returned image is whatever imaging.Fetcher produces (PNG/JPEG/GIF/etc.).
func (p *Pool) Fetch(ctx context.Context, url string) (image.Image, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(p.slotTimeout):
		return nil, ErrSlotTimeout
	}
	defer func() { <-p.sem }()

	fctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()
	img, err := p.fetcher.Fetch(fctx, url)
	if err != nil {
		if p.logger != nil {
			p.logger.Debug("asyncfetch: fetch", "url", url, "err", err)
		}
		return nil, err
	}
	return img, nil
}
