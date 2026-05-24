package screens

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/imaging"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/chat"
)

// imageRenderCols is the half-block width the chat surface targets for an
// inline image. Capped so a big picture doesn't crowd the body column.
const imageRenderCols = 40

// imageFetchedMsg carries the result of a URL fetch+render back to the Update
// loop. `Lines` is nil on failure (fetch error / non-image / oversized); the
// handler still drains the pending message set so we don't retry endlessly.
// `Channel` is the channel the URL was first seen in — used so the fetcher
// only repaints into the same channel's log (a paste cross-posted to two
// channels would otherwise duplicate the picture on a switch).
type imageFetchedMsg struct {
	URL       string
	ChannelID int64
	Lines     []string
}

// pfpResolvedMsg lands after a lazy ProfileService.GetByHandle for a handle
// that wasn't covered by the bootstrap snapshot. The chat log absorbs the
// boolean and (if true) rewraps the matching entries so the "●" appears.
type pfpResolvedMsg struct {
	Handle string
	Has    bool
}

// scheduleHistoryImageFetches walks every loaded history message, scanning
// the body for image URLs, and returns the batch of fetch Cmds. Cached URLs
// attach inline immediately (so on a /switch back the picture re-paints
// without a network roundtrip).
func (m *Chat) scheduleHistoryImageFetches(channelID int64, hist []realtime.Message) []tea.Cmd {
	if len(hist) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, h := range hist {
		if h.ID == 0 || !h.DeletedAt.IsZero() {
			continue
		}
		cmds = append(cmds, m.scheduleImageFetches(channelID, h.ID, h.Body)...)
	}
	return cmds
}

// scheduleImageFetches scans the message body for image URLs and either
// (a) immediately attaches a cached render, (b) piggy-backs onto an in-flight
// fetch's pending list, or (c) starts a new fetch. Returns the batch of
// tea.Cmds to dispatch — usually one per newly-scheduled URL, or empty when
// every URL was already cached.
func (m *Chat) scheduleImageFetches(channelID, messageID int64, body string) []tea.Cmd {
	urls := chat.ExtractImageURLs(body)
	if len(urls) == 0 {
		return nil
	}
	log := m.logFor(channelID)
	var cmds []tea.Cmd
	m.imageMu.Lock()
	for _, url := range urls {
		// Cache hit (non-nil lines) — attach inline without scheduling.
		// Nil-lines hits are cached failures: drop silently.
		if cached, ok := m.imageCache.Peek(url); ok {
			m.imageMu.Unlock()
			if len(cached) > 0 {
				log.AttachImage(messageID, cached)
			}
			m.imageMu.Lock()
			continue
		}
		if pending, ok := m.pendingFetches[url]; ok {
			m.pendingFetches[url] = append(pending, messageID)
			continue
		}
		m.pendingFetches[url] = []int64{messageID}
		cmds = append(cmds, m.fetchImage(channelID, url))
	}
	m.imageMu.Unlock()
	return cmds
}

// fetchImage performs the URL fetch through the shared sess.Images pool
// (caps process-wide concurrency + singleflight-coalesces duplicates) and
// returns a Cmd that emits imageFetchedMsg with the rendered halfblock
// lines. Failures cache as nil-lines so repeat references render the
// placeholder without retrying.
func (m *Chat) fetchImage(channelID int64, url string) tea.Cmd {
	parent := m.sess.Ctx()
	return func() tea.Msg {
		lines, _ := m.imageCache.Get(parent, url, func(ctx context.Context) ([]string, error) {
			img, err := m.sess.Images.Fetch(ctx, url)
			if err != nil {
				return nil, nil
			}
			return imaging.RenderToANSILines(img, imageRenderCols), nil
		})
		return imageFetchedMsg{URL: url, ChannelID: channelID, Lines: lines}
	}
}

// resolvePfp asks ProfileService whether `handle` has a profile picture
// uploaded. Result lands as pfpResolvedMsg and SetPfp on every log. Safe to
// fire-and-forget; missing ProfileService (test paths) collapses to a no-op.
func (m *Chat) resolvePfp(handle string) tea.Cmd {
	if m.sess.Profile == nil || handle == "" {
		return nil
	}
	profile := m.sess.Profile
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(2 * time.Second)
		defer cancel()
		snap, err := profile.GetByHandle(ctx, handle)
		if err != nil {
			m.sess.Logger.Warn("chat: resolve pfp", "handle", handle, "err", err)
			return nil
		}
		has := snap != nil && snap.HasPfp()
		return pfpResolvedMsg{Handle: handle, Has: has}
	}
}
