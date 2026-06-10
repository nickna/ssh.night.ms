package screens

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/providers/ttlcache"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/chat"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Chat is the multi-channel chat screen. One merged subscription covers
// every channel the user is in, so messages in non-active channels arrive
// in real time and surface as unread badges in the sidebar without
// requiring the user to /switch first.
type Chat struct {
	sess *session.Session
	chat *realtime.ChatService

	logs   map[int64]*components.ChatLog
	joined []gen.Channel // raw rows; sidebar items computed in toListItems each frame
	active chatChannelHandle

	// Per-channel unread message counter. Incremented on chatEventMsg for
	// channels other than active; reset when the user switches to a channel.
	unread map[int64]int

	input textinput.Model

	// Merged subscription across every joined channel. Re-fanout on /join.
	subCtx    context.Context
	subCancel context.CancelFunc
	subStream <-chan realtime.ChatEvent

	// onlinePartners caches the most-recent presence read for the DM partners
	// in our joined list. Refreshed every 30s via a tea.Tick so the sidebar
	// dots stay roughly in sync with the 60s presence TTL without paying a
	// Redis roundtrip on every render.
	onlinePartners map[string]bool

	// typing tracks the "X is typing" indicator state per channel.
	// typing[channelID][handle] = expiresAt. Entries past expiresAt are pruned
	// on each typingTickMsg. The publisher's throttle is typingPublishedAt.
	typing             map[int64]map[string]time.Time
	typingPublishedAt  time.Time
	lastInputValue     string

	// onlineHandles is the right-rail roster of currently-online users.
	// Refreshed every onlineRefreshInterval via tea.Tick — the call is one
	// Redis SCAN so we don't poll too aggressively.
	onlineHandles []string

	// mentionFlashUntil is the deadline up to which the status row shows
	// "@ you were mentioned". Set whenever a live incoming message from
	// another user contains an @selfHandle token.
	mentionFlashUntil time.Time

	// Inline-image plumbing. imageCache memoizes rendered half-block lines
	// across the session (TTL=0 — image renders don't go stale; a fetch
	// failure is cached as nil-lines so retries don't fire every paint).
	// pendingFetches tracks the message IDs waiting on each in-flight URL
	// so when the fetch lands, all messages that referenced the same picture
	// repaint in one pass. imageMu only guards pendingFetches now; the
	// cache + concurrency cap moved to sess.Images (shared with browser).
	imageCache     *ttlcache.Cache[string, []string]
	pendingFetches map[string][]int64
	imageMu        sync.Mutex

	errMsg string
}

type chatChannelHandle struct {
	ID    int64
	Name  string
	Topic string
}

func NewChat(sess *session.Session, chatSvc *realtime.ChatService) tea.Model {
	in := textinput.New()
	in.Placeholder = "say something — try /help"
	in.CharLimit = 4000
	in.Focus()
	return &Chat{
		sess:           sess,
		chat:           chatSvc,
		logs:           make(map[int64]*components.ChatLog),
		unread:         make(map[int64]int),
		onlinePartners: make(map[string]bool),
		typing:         make(map[int64]map[string]time.Time),
		input:          in,
		imageCache:     ttlcache.New[string, []string](0, nil),
		pendingFetches: make(map[string][]int64),
	}
}

//
// tea.Msg envelopes
//

type chatBootstrapMsg struct {
	joined       []gen.Channel
	active       chatChannelHandle
	sub          chatSubBundle
	hist         []realtime.Message
	unread       map[int64]int // persisted unread counts at startup
	reactions    map[int64]map[string]int
	replyCounts  map[int64]int  // per-parent reply totals for hist window
	pfpByHandle  map[string]bool // lowercased handles → "has pfp"
}

// chatRefanMsg replaces the multi-channel subscription after a /join added
// a new channel that should now be covered.
type chatRefanMsg struct {
	joined       []gen.Channel
	active       chatChannelHandle
	sub          chatSubBundle
	hist         []realtime.Message
	reactions    map[int64]map[string]int
	replyCounts  map[int64]int
	pfpByHandle  map[string]bool
}

// chatLocalSwitchMsg is a pure UI swap to an already-joined channel; no
// subscription teardown, no re-fanout. Loads history on first visit.
type chatLocalSwitchMsg struct {
	active       chatChannelHandle
	hist         []realtime.Message // nil if log already loaded
	reactions    map[int64]map[string]int
	replyCounts  map[int64]int
	pfpByHandle  map[string]bool
}

type chatSubBundle struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream <-chan realtime.ChatEvent
}

type chatEventMsg realtime.ChatEvent

type chatErrMsg struct {
	stage string
	err   error
}

type chatNoticeMsg struct{ text string }

func (m *Chat) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.bootstrap(),
		m.scheduleOnlineRefresh(),
		m.scheduleTypingTick(),
		m.scheduleOnlineRoster(),
	)
}

// bootstrap resolves #lobby, ensures membership, loads joined channels, opens
// a merged subscription covering every joined channel, then seeds the lobby
// log with its history.
func (m *Chat) bootstrap() tea.Cmd {
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx := m.sess.Ctx()
		ch, err := m.chat.ResolvePublicChannel(ctx, "lobby", userID)
		if err != nil {
			return chatErrMsg{stage: "resolve lobby", err: err}
		}
		if err := m.chat.EnsureMembership(ctx, ch.ID, userID); err != nil {
			return chatErrMsg{stage: "join lobby", err: err}
		}
		joined, err := m.chat.JoinedChannels(ctx, userID)
		if err != nil {
			return chatErrMsg{stage: "list channels", err: err}
		}
		unread, err := m.chat.UnreadCounts(ctx, userID)
		if err != nil {
			return chatErrMsg{stage: "unread counts", err: err}
		}
		sub, err := openMergedSub(m.sess.Ctx(), m.chat, channelIDs(joined))
		if err != nil {
			return chatErrMsg{stage: "subscribe", err: err}
		}
		hist, err := m.chat.RecentMessages(ctx, ch.ID, 100)
		if err != nil {
			sub.cancel()
			return chatErrMsg{stage: "load history", err: err}
		}
		reactions, err := m.chat.ReactionsForChannel(ctx, ch.ID)
		if err != nil {
			sub.cancel()
			return chatErrMsg{stage: "load reactions", err: err}
		}
		replyCounts, pfpMap := m.loadDecorations(ctx, hist)
		return chatBootstrapMsg{
			joined:       joined,
			active:       chatChannelHandle{ID: ch.ID, Name: ch.Name, Topic: derefTopic(ch.Topic)},
			sub:          sub,
			hist:         hist,
			unread:       unread,
			reactions:    reactions,
			replyCounts:  replyCounts,
			pfpByHandle:  pfpMap,
		}
	}
}

// loadDecorations loads reply-count + has-pfp snapshots for the visible
// window of messages. Best-effort: each call sub-logs and returns the zero
// value on failure so a slow secondary table doesn't block chat startup.
func (m *Chat) loadDecorations(ctx context.Context, hist []realtime.Message) (map[int64]int, map[string]bool) {
	replyCounts := map[int64]int{}
	pfpMap := map[string]bool{}
	if len(hist) == 0 {
		return replyCounts, pfpMap
	}
	ids := make([]int64, 0, len(hist))
	handles := make([]string, 0, len(hist))
	seenHandles := make(map[string]bool, len(hist))
	for _, msg := range hist {
		if msg.ID != 0 {
			ids = append(ids, msg.ID)
		}
		key := strings.ToLower(msg.Handle)
		if msg.Handle != "" && !seenHandles[key] {
			seenHandles[key] = true
			handles = append(handles, msg.Handle)
		}
	}
	if len(ids) > 0 {
		c, err := m.chat.ReplyCounts(ctx, ids)
		if err != nil {
			m.sess.Logger.Warn("chat: reply counts", "err", err)
		} else {
			replyCounts = c
		}
	}
	if m.sess.Profile != nil && len(handles) > 0 {
		p, err := m.sess.Profile.BatchHasPfp(ctx, handles)
		if err != nil {
			m.sess.Logger.Warn("chat: batch has pfp", "err", err)
		} else {
			pfpMap = p
			// Backfill "false" for handles we asked about but didn't get a
			// row for — that prevents repeated lookups for never-seen users.
			for _, h := range handles {
				key := strings.ToLower(h)
				if _, ok := pfpMap[key]; !ok {
					pfpMap[key] = false
				}
			}
		}
	}
	return replyCounts, pfpMap
}

// touchRead writes a last_read marker for the channel asynchronously. Used on
// every switch and on every message-in-active-channel — the SQL is monotonic
// (GREATEST) so concurrent touches can't move the marker backward.
func (m *Chat) touchRead(channelID, msgID int64) tea.Cmd {
	if channelID == 0 {
		return nil
	}
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		if err := m.chat.TouchChannelRead(ctx, userID, channelID, msgID); err != nil {
			m.sess.Logger.Warn("chat: persist read state", "channel_id", channelID, "err", err)
		}
		return nil
	}
}

// touchActiveAtLatest fetches the channel's latest message id and persists
// it as last_read. Used after a switch into a channel where we don't have
// the latest id cached.
func (m *Chat) touchActiveAtLatest(channelID int64) tea.Cmd {
	if channelID == 0 {
		return nil
	}
	userID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3*time.Second)
		defer cancel()
		latest, err := m.chat.LatestMessageID(ctx, channelID)
		if err != nil {
			m.sess.Logger.Warn("chat: latest msg id", "channel_id", channelID, "err", err)
			return nil
		}
		if err := m.chat.TouchChannelRead(ctx, userID, channelID, latest); err != nil {
			m.sess.Logger.Warn("chat: persist read state", "channel_id", channelID, "err", err)
		}
		return nil
	}
}

// joinChannel creates/joins the named channel, refreshes the joined list,
// tears down the existing subscription, and opens a new merged sub that
// covers the new channel too. Used by /join and the post-/dm flow.
func (m *Chat) joinChannel(name string) tea.Cmd {
	userID := m.sess.Identity.UserID
	oldCancel := m.subCancel
	return func() tea.Msg {
		if oldCancel != nil {
			oldCancel()
		}
		ctx := m.sess.Ctx()
		ch, err := m.chat.ResolvePublicChannel(ctx, name, userID)
		if err != nil {
			return chatErrMsg{stage: "resolve " + name, err: err}
		}
		if err := m.chat.EnsureMembership(ctx, ch.ID, userID); err != nil {
			return chatErrMsg{stage: "join " + name, err: err}
		}
		joined, err := m.chat.JoinedChannels(ctx, userID)
		if err != nil {
			return chatErrMsg{stage: "list channels", err: err}
		}
		sub, err := openMergedSub(m.sess.Ctx(), m.chat, channelIDs(joined))
		if err != nil {
			return chatErrMsg{stage: "subscribe " + name, err: err}
		}
		// History only needed if the screen hasn't visited this channel yet.
		// The Update loop will skip the AppendAll when log was already populated.
		hist, err := m.chat.RecentMessages(ctx, ch.ID, 100)
		if err != nil {
			sub.cancel()
			return chatErrMsg{stage: "load history " + name, err: err}
		}
		reactions, err := m.chat.ReactionsForChannel(ctx, ch.ID)
		if err != nil {
			sub.cancel()
			return chatErrMsg{stage: "load reactions " + name, err: err}
		}
		replyCounts, pfpMap := m.loadDecorations(ctx, hist)
		return chatRefanMsg{
			joined:       joined,
			active:       chatChannelHandle{ID: ch.ID, Name: ch.Name, Topic: derefTopic(ch.Topic)},
			sub:          sub,
			hist:         hist,
			reactions:    reactions,
			replyCounts:  replyCounts,
			pfpByHandle:  pfpMap,
		}
	}
}

// switchChannel is a pure UI swap to an already-joined channel — no
// subscription work. Returns an error notice if the user isn't a member.
func (m *Chat) switchChannel(name string) tea.Cmd {
	target := m.findJoined(name)
	if target == nil {
		return m.notice(fmt.Sprintf("not a member of #%s — use /join first", name))
	}
	handle := chatChannelHandle{ID: target.ID, Name: target.Name, Topic: derefTopic(target.Topic)}
	// If we already have a log for this channel, skip history load. The first
	// /switch into a channel still needs history because nothing populated
	// the log yet (events only land for subscribed channels — they do, but
	// without history the user would only see messages that arrived during
	// this session).
	_, alreadyLoaded := m.logs[target.ID]
	return func() tea.Msg {
		if alreadyLoaded {
			return chatLocalSwitchMsg{active: handle}
		}
		ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
		defer cancel()
		hist, err := m.chat.RecentMessages(ctx, target.ID, 100)
		if err != nil {
			return chatErrMsg{stage: "load history " + name, err: err}
		}
		reactions, err := m.chat.ReactionsForChannel(ctx, target.ID)
		if err != nil {
			return chatErrMsg{stage: "load reactions " + name, err: err}
		}
		replyCounts, pfpMap := m.loadDecorations(ctx, hist)
		return chatLocalSwitchMsg{
			active:       handle,
			hist:         hist,
			reactions:    reactions,
			replyCounts:  replyCounts,
			pfpByHandle:  pfpMap,
		}
	}
}

func (m *Chat) findJoined(name string) *gen.Channel {
	for i := range m.joined {
		if strings.EqualFold(m.joined[i].Name, name) {
			return &m.joined[i]
		}
	}
	return nil
}

func (m *Chat) leaveCurrent() tea.Cmd {
	if m.active.Name == "lobby" {
		return m.notice("can't leave #lobby — type /quit to log out")
	}
	currentID := m.active.ID
	userID := m.sess.Identity.UserID
	// LeaveMembership then re-fanout (so the now-departed channel falls out
	// of the merged sub) and switch active back to #lobby.
	return tea.Sequence(
		func() tea.Msg {
			ctx, cancel := m.sess.CtxWithTimeout(5*time.Second)
			defer cancel()
			if err := m.chat.LeaveMembership(ctx, currentID, userID); err != nil {
				return chatErrMsg{stage: "leave", err: err}
			}
			return nil
		},
		m.joinChannel("lobby"), // re-resolve + ensure + refanout; lobby is the fallback
	)
}

// openMergedSub starts the merged Redis subscription bound to parent — the
// session SSH/WS context, NOT context.Background. If the user disconnects
// mid-chat the parent cancels, which unwinds the subscription goroutine in
// realtime and frees the Redis sub. The returned cancel is still called on
// /switch + /leave for prompt teardown without waiting for disconnect.
func openMergedSub(parent context.Context, svc *realtime.ChatService, channelIDs []int64) (chatSubBundle, error) {
	ctx, cancel := context.WithCancel(parent)
	stream, err := svc.SubscribeChannels(ctx, channelIDs)
	if err != nil {
		cancel()
		return chatSubBundle{}, err
	}
	return chatSubBundle{ctx: ctx, cancel: cancel, stream: stream}, nil
}

func channelIDs(chans []gen.Channel) []int64 {
	out := make([]int64, len(chans))
	for i, c := range chans {
		out[i] = c.ID
	}
	return out
}

func waitForEvent(stream <-chan realtime.ChatEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-stream
		if !ok {
			return nil
		}
		return chatEventMsg(ev)
	}
}

func (m *Chat) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.relayout()

	case chatBootstrapMsg:
		m.joined = msg.joined
		m.active = msg.active
		m.subCtx, m.subCancel, m.subStream = msg.sub.ctx, msg.sub.cancel, msg.sub.stream
		m.unread = msg.unread
		if m.unread == nil {
			m.unread = make(map[int64]int)
		}
		m.unread[msg.active.ID] = 0 // active channel is being viewed now
		m.seedChannelLog(msg.active.ID, msg.hist, msg.pfpByHandle, msg.reactions, msg.replyCounts, true)
		// Schedule image fetches for every history message that references one.
		imgCmds := m.scheduleHistoryImageFetches(msg.active.ID, msg.hist)
		// Kick off an immediate presence read so the sidebar dots aren't
		// stuck at "offline" until the 30s tick fires; same for the right-
		// rail roster so it isn't blank until the 15s tick.
		startup := []tea.Cmd{
			waitForEvent(m.subStream),
			m.touchActiveAtLatest(msg.active.ID),
			m.refreshPartnerPresence(),
			m.refreshOnlineRoster(),
		}
		startup = append(startup, imgCmds...)
		return m, tea.Batch(startup...)

	case chatRefanMsg:
		m.joined = msg.joined
		m.active = msg.active
		m.subCtx, m.subCancel, m.subStream = msg.sub.ctx, msg.sub.cancel, msg.sub.stream
		m.unread[msg.active.ID] = 0
		// AppendAll is idempotent on ID (the indexByID map dedupes), so
		// re-fanout into a previously-visited channel doesn't double-paint.
		m.seedChannelLog(msg.active.ID, msg.hist, msg.pfpByHandle, msg.reactions, msg.replyCounts, true)
		m.relayout()
		imgCmds := m.scheduleHistoryImageFetches(msg.active.ID, msg.hist)
		out := []tea.Cmd{
			waitForEvent(m.subStream),
			m.touchActiveAtLatest(msg.active.ID),
			m.refreshPartnerPresence(),
		}
		out = append(out, imgCmds...)
		return m, tea.Batch(out...)

	case chatLocalSwitchMsg:
		m.active = msg.active
		m.unread[msg.active.ID] = 0
		m.seedChannelLog(msg.active.ID, msg.hist, msg.pfpByHandle, msg.reactions, msg.replyCounts, false)
		m.relayout()
		imgCmds := m.scheduleHistoryImageFetches(msg.active.ID, msg.hist)
		if len(imgCmds) > 0 {
			out := []tea.Cmd{m.touchActiveAtLatest(msg.active.ID)}
			out = append(out, imgCmds...)
			return m, tea.Batch(out...)
		}
		return m, m.touchActiveAtLatest(msg.active.ID)

	case chatEventMsg:
		switch msg.Kind {
		case realtime.ChatEventMessageCreated:
			log := m.logFor(msg.ChannelID)
			log.SetSelfHandle(m.sess.Identity.Handle)
			mentioned := log.Append(realtime.Message{
				ID:              msg.MessageID,
				ChannelID:       msg.ChannelID,
				UserID:          msg.UserID,
				Handle:          msg.Handle,
				IsSysop:         msg.IsSysop,
				Body:            msg.Body,
				CreatedAt:       msg.CreatedAt,
				ParentMessageID: msg.ParentMessageID,
			})
			// Self-mention flash: only flag when somebody else mentions us —
			// echoing our own message back shouldn't trigger the badge.
			if mentioned && msg.UserID != m.sess.Identity.UserID {
				m.mentionFlashUntil = time.Now().Add(mentionFlashDuration)
			}
			// Lazy PFP fetch: if we've never seen this handle, ask the
			// profile service once and cache the result. The next message
			// from this handle (or the rewrap from SetPfp) shows the dot.
			var pfpCmd tea.Cmd
			if _, known := log.HasPfp(msg.Handle); !known {
				pfpCmd = m.resolvePfp(msg.Handle)
			}
			// Inline images: scan the body for image URLs and either attach
			// the cached render or kick off fetches.
			imgCmds := m.scheduleImageFetches(msg.ChannelID, msg.MessageID, msg.Body)
			if msg.ChannelID == m.active.ID {
				log.SnapToBottom()
				// Advance persisted read state as messages arrive so the
				// badge stays at 0 across reconnects.
				cmds := []tea.Cmd{waitForEvent(m.subStream), m.touchRead(msg.ChannelID, msg.MessageID)}
				if pfpCmd != nil {
					cmds = append(cmds, pfpCmd)
				}
				cmds = append(cmds, imgCmds...)
				return m, tea.Batch(cmds...)
			} else if msg.UserID != m.sess.Identity.UserID {
				// Only count messages we didn't send ourselves; otherwise the
				// echo-back of our own /send-while-in-other-channel would
				// inflate the badge.
				m.unread[msg.ChannelID]++
			}
			if pfpCmd != nil || len(imgCmds) > 0 {
				cmds := []tea.Cmd{waitForEvent(m.subStream)}
				if pfpCmd != nil {
					cmds = append(cmds, pfpCmd)
				}
				cmds = append(cmds, imgCmds...)
				return m, tea.Batch(cmds...)
			}
		case realtime.ChatEventReactionAdded:
			m.logFor(msg.ChannelID).AddReaction(msg.MessageID, msg.Emoji)
		case realtime.ChatEventReactionRemoved:
			m.logFor(msg.ChannelID).RemoveReaction(msg.MessageID, msg.Emoji)
		case realtime.ChatEventMessageEdited:
			log := m.logFor(msg.ChannelID)
			log.Append(realtime.Message{
				ID:        msg.MessageID,
				ChannelID: msg.ChannelID,
				UserID:    msg.UserID,
				Handle:    msg.Handle,
				IsSysop:   msg.IsSysop,
				Body:      msg.Body,
				CreatedAt: msg.CreatedAt,
				EditedAt:  msg.EditedAt,
			})
		case realtime.ChatEventTyping:
			// Ignore our own echoes — we don't render "you're typing" for self.
			if msg.UserID == m.sess.Identity.UserID {
				break
			}
			perChannel := m.typing[msg.ChannelID]
			if perChannel == nil {
				perChannel = make(map[string]time.Time)
				m.typing[msg.ChannelID] = perChannel
			}
			perChannel[msg.Handle] = time.Now().Add(typingTTL)
		case realtime.ChatEventMessageDeleted:
			m.logFor(msg.ChannelID).MarkDeleted(msg.MessageID, time.Now().UTC())
		case realtime.ChatEventPinChanged:
			m.logFor(msg.ChannelID).MarkPinned(msg.MessageID, msg.IsPinned)
		case realtime.ChatEventTopicChanged:
			if msg.ChannelID == m.active.ID {
				if msg.Topic == nil {
					m.active.Topic = ""
				} else {
					m.active.Topic = *msg.Topic
				}
			}
			// Also patch the cached entry in the joined list so a future
			// switch back picks up the new value.
			for i := range m.joined {
				if m.joined[i].ID == msg.ChannelID {
					m.joined[i].Topic = msg.Topic
				}
			}
		}
		return m, waitForEvent(m.subStream)

	case typingTickMsg:
		// Prune anything past TTL so the footer doesn't get stuck on the
		// indicator after the typist stops.
		now := time.Now()
		for chID, perChannel := range m.typing {
			for handle, exp := range perChannel {
				if now.After(exp) {
					delete(perChannel, handle)
				}
			}
			if len(perChannel) == 0 {
				delete(m.typing, chID)
			}
		}
		return m, m.scheduleTypingTick()

	case chatErrMsg:
		m.errMsg = fmt.Sprintf("%s: %v", msg.stage, msg.err)
		m.sess.Logger.Error("chat screen error", "stage", msg.stage, "err", msg.err)

	case chatNoticeMsg:
		return m, m.notice(msg.text)

	case presenceTickMsg:
		return m, tea.Batch(m.refreshPartnerPresence(), m.scheduleOnlineRefresh())

	case presenceOnlineMsg:
		m.onlinePartners = msg

	case onlineTickMsg:
		return m, tea.Batch(m.refreshOnlineRoster(), m.scheduleOnlineRoster())

	case onlineRosterMsg:
		m.onlineHandles = msg

	case pfpResolvedMsg:
		// Push into every loaded log so all open channels learn the value at
		// once. Cheap — the rewrap is per-message and only triggers on
		// matching entries.
		for _, log := range m.logs {
			log.SetPfp(msg.Handle, msg.Has)
		}

	case imageFetchedMsg:
		// Drain the pending message list under the lock so a concurrent
		// scheduler either sees the URL still pending (and queues itself)
		// or gone (and falls back to the cache check, which sees the
		// successful render via Peek). The cache itself (sess-scoped TTL
		// cache) already stored the result inside fetchImage; failures
		// stored as nil-lines stop the next paint from retrying.
		m.imageMu.Lock()
		pending := m.pendingFetches[msg.URL]
		delete(m.pendingFetches, msg.URL)
		m.imageMu.Unlock()
		if msg.Lines == nil {
			break
		}
		log := m.logFor(msg.ChannelID)
		for _, mid := range pending {
			log.AttachImage(mid, msg.Lines)
		}

	case chatDMReadyMsg:
		// ResolveDM already added both memberships, so joinChannel here is
		// idempotent on the DB side; its real job is to re-fanout the
		// subscription so the DM channel falls into the merged stream and
		// to set it as active.
		return m, m.joinChannel(msg.channelName)

	case tea.MouseMsg:
		if cmd := m.handleMouse(msg); cmd != nil {
			return m, cmd
		}

	case tea.KeyMsg:
		// Alt+digit jumps to the Nth joined channel. Alt+0 maps to slot 10
		// so the keymap mirrors what the sidebar shows. The non-Alt digits
		// stay free for typing into the input.
		if slot, ok := altDigitSlot(msg.String()); ok {
			if slot < len(m.joined) && m.joined[slot].ID != m.active.ID {
				return m, m.switchChannel(m.joined[slot].Name)
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			// Thread-mode Esc clears the filter rather than leaving chat —
			// matches Slack/Discord muscle memory for "back out of thread".
			if log := m.activeLog(); log != nil && log.ThreadFilter() != 0 {
				log.SetThreadFilter(0)
				return m, nil
			}
			if m.subCancel != nil {
				m.subCancel()
			}
			return m, nav.Navigate(nav.DestLobby)
		case "pgup":
			m.activeLog().ScrollUp(m.logHeight() - 1)
			return m, nil
		case "pgdown":
			m.activeLog().ScrollDown(m.logHeight() - 1)
			return m, nil
		case "enter":
			return m, m.submit()
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Detect "user is actively composing" — fire a typing event on the active
	// channel if the input text changed AND we haven't published recently.
	// Slash-commands suppress: showing "alice is typing" while alice runs
	// /help isn't useful. The throttle keeps publish cadence at most one per
	// typingPublishInterval; the receiver's TTL covers the gap.
	if m.active.ID != 0 {
		val := m.input.Value()
		if val != m.lastInputValue {
			m.lastInputValue = val
			if val != "" && !strings.HasPrefix(val, "/") {
				if time.Since(m.typingPublishedAt) >= typingPublishInterval {
					m.typingPublishedAt = time.Now()
					if pub := m.publishTyping(m.active.ID); pub != nil {
						return m, tea.Batch(cmd, pub)
					}
				}
			}
		}
	}
	return m, cmd
}

func (m *Chat) submit() tea.Cmd {
	raw := strings.TrimSpace(m.input.Value())
	if raw == "" || m.active.ID == 0 {
		return nil
	}
	m.input.SetValue("")
	if c, ok := chat.Parse(raw); ok {
		return m.runCommand(c)
	}
	return m.sendChat(raw)
}


func (m *Chat) notice(text string) tea.Cmd {
	m.activeLog().AppendSystem(text)
	m.activeLog().SnapToBottom()
	return nil
}

//
// layout helpers
//

func (m *Chat) activeLog() *components.ChatLog { return m.logFor(m.active.ID) }

func (m *Chat) logFor(channelID int64) *components.ChatLog {
	if log, ok := m.logs[channelID]; ok {
		return log
	}
	log := components.NewChatLog()
	log.SetSize(m.bodyWidth(), m.logHeight())
	// Route message-header timestamps through the user's display prefs so
	// 12-hour-preference users see "1:07 PM" instead of the default 24h.
	log.SetTimeFormatter(m.sess.DisplayPrefs.FormatClock)
	m.logs[channelID] = log
	return log
}

// seedChannelLog applies a freshly-fetched history payload to channelID's
// log: self handle, PFP marks (set before AppendAll so the first paint
// already carries ● marks), history, reactions, reply counts, then snaps to
// the bottom. Shared by the bootstrap / refan / local-switch handlers.
//
// replaceReactions controls the nil-snapshot case: bootstrap and refan fetch
// a full reaction snapshot per call, so they replace unconditionally (a nil
// payload legitimately clears the overlay); a local switch into an
// already-seeded channel passes false so a nil payload doesn't wipe chips
// that are still live.
func (m *Chat) seedChannelLog(
	channelID int64,
	hist []realtime.Message,
	pfpByHandle map[string]bool,
	reactions map[int64]map[string]int,
	replyCounts map[int64]int,
	replaceReactions bool,
) {
	log := m.logFor(channelID)
	log.SetSelfHandle(m.sess.Identity.Handle)
	if len(pfpByHandle) > 0 {
		log.SetPfpHandles(pfpByHandle)
	}
	log.AppendAll(hist)
	if replaceReactions || reactions != nil {
		log.SetReactions(reactions)
	}
	if len(replyCounts) > 0 {
		log.SetReplyCounts(replyCounts)
	}
	log.SnapToBottom()
}

func (m *Chat) relayout() {
	if l, ok := m.logs[m.active.ID]; ok {
		l.SetSize(m.bodyWidth(), m.logHeight())
	}
	m.input.Width = m.bodyWidth() - 4
}

func (m *Chat) bodyWidth() int {
	// Chrome: left channels sidebar + gutter + right online sidebar + gutter.
	chrome := components.ChannelList{}.Width() + 1 + components.OnlineList{}.Width() + 1
	w := m.sess.Width - chrome
	if w < 20 {
		return 20
	}
	return w
}

// availableHeight is the row count the chat screen actually owns. Root
// reserves the bottom row for its persistent status bar (and another for the
// wall banner when one is active, but the chat screen doesn't know about
// banners — we leave one extra row of give to avoid clipping in that case).
func (m *Chat) availableHeight() int {
	h := m.sess.Height - 1
	if h < 1 {
		return 1
	}
	return h
}

// logHeight is whatever's left after subtracting the chat's own chrome from
// availableHeight. Chrome is header(1) + status(1) + input(3 with rounded
// border) = 5, plus an extra row for the input preview when it's visible
// (the preview row is omitted from the View entirely when empty so we don't
// waste it on rest state).
func (m *Chat) logHeight() int {
	chrome := 5
	if m.previewVisible() {
		chrome++
	}
	h := m.availableHeight() - chrome
	if h < 1 {
		return 1
	}
	return h
}

// previewVisible reports whether the input preview row will render this frame.
// Centralized so logHeight + View agree on which layout shape they're producing.
func (m *Chat) previewVisible() bool {
	return strings.TrimSpace(m.input.Value()) != ""
}

//
// view
//

var (
	chatHeaderStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	chatStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
	chatPreviewCmd     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Bold(true)
	chatPreviewMention = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow)).Bold(true)
	chatPreviewChannel = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccent)).Bold(true)
	chatPreviewBody    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	chatPreviewMe      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan)).Italic(true)
	chatInputFrame     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(theme.ColorDim)).Padding(0, 1)
	chatGutterStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))

	// Status row chrome: dim "in #lobby topic:" framing + italic "| typing"
	// suffix.
	chatStatusChrome       = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	chatStatusChromeItalic = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim)).Italic(true)
	// Mention flash — bright yellow + bold so it overwrites the dim chrome
	// and reads as an alert.
	chatMentionFlashStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
)

func (m *Chat) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing chat..."
	}
	m.relayout()

	leftSidebar := components.ChannelList{
		Items:    m.sidebarItems(),
		ActiveID: m.active.ID,
	}.View(m.availableHeight())

	rightSidebar := components.OnlineList{
		Handles: m.onlineHandles,
		Self:    m.sess.Identity.Handle,
	}.View(m.availableHeight())

	name := m.active.Name
	if name == "" {
		name = "..."
	}
	// DM channels render the partner's handle in the body header too.
	displayName := "#" + name
	if other, ok := dmPartner(name, m.sess.Identity.Handle); ok {
		displayName = "@" + other
	}
	header := chatHeaderStyle.Render(displayName) + "  "
	if log := m.activeLog(); log != nil && log.ThreadFilter() != 0 {
		header += chatStatusStyle.Render(fmt.Sprintf("thread #%d  ", log.ThreadFilter()))
		header += theme.Hint.Render("Esc: exit thread · PgUp/PgDn: scroll · /help")
	} else {
		header += theme.Hint.Render("Esc: lobby · PgUp/PgDn: scroll · /help")
	}

	body := m.activeLog().View()

	status := m.buildStatusRow()

	input := chatInputFrame.Render(m.input.View())
	parts := []string{header, body, status}
	if preview := m.renderInputPreview(m.bodyWidth()); preview != "" {
		parts = append(parts, preview)
	}
	parts = append(parts, input)

	rightCol := lipgloss.JoinVertical(lipgloss.Left, parts...)
	// Gutter: build exactly availableHeight rows so it aligns with the
	// sidebars + body without spilling past the bottom edge (where the root
	// status bar lives).
	gutterRows := make([]string, m.availableHeight())
	for i := range gutterRows {
		gutterRows[i] = "│"
	}
	gutter := chatGutterStyle.Render(strings.Join(gutterRows, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, leftSidebar, gutter, rightCol, gutter, rightSidebar)
}

// buildStatusRow assembles the styled "in #lobby  topic: <topic>  |  alice is
// typing…" line shown between the chat body and the input. Self-mention flash
// + transient error wins over typing and the static topic line.
func (m *Chat) buildStatusRow() string {
	if m.errMsg != "" {
		return chatStatusStyle.Render("! " + m.errMsg)
	}
	if time.Now().Before(m.mentionFlashUntil) {
		return chatMentionFlashStyle.Render("@ you were mentioned")
	}

	chanLabel := "#" + m.active.Name
	if other, ok := dmPartner(m.active.Name, m.sess.Identity.Handle); ok {
		chanLabel = "@" + other
	} else if m.active.Name == "" {
		chanLabel = "(none)"
	}
	prefix := chatStatusChrome.Render("in " + chanLabel + "  topic: ")

	var topicPart string
	if m.active.Topic == "" {
		topicPart = chatStatusChromeItalic.Render("(none)")
	} else {
		// Run the topic through the same body tokenizer the chat log uses so
		// *bold*/_italic_/`code`/:emoji: render consistently in topic and
		// message body. Cap the width so a runaway topic doesn't push the
		// typing hint off-screen.
		budget := m.bodyWidth() - lipgloss.Width(prefix) - 24
		if budget < 8 {
			budget = 8
		}
		topicLines, _ := chat.WrapBodyLines(m.active.Topic, m.sess.Identity.Handle, budget)
		topicPart = topicLines[0]
	}

	out := prefix + topicPart
	if typers := m.typingFooter(); typers != "" {
		out += chatStatusChromeItalic.Render("  |  " + typers)
	}
	return out
}

// handleMouse decides whether a mouse event should drive a screen action.
// Left-click + release inside the left channel sidebar's row band switches to
// the clicked channel; wheel-up/wheel-down over the chat body scrolls the
// active log. Returns the tea.Cmd to dispatch, or nil when the event is
// uninteresting (textinput handles its own focus).
func (m *Chat) handleMouse(msg tea.MouseMsg) tea.Cmd {
	// Wheel events: scroll the chat body. Wheel-up = older, wheel-down = newer.
	if msg.Button == tea.MouseButtonWheelUp {
		m.activeLog().ScrollUp(3)
		return nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		m.activeLog().ScrollDown(3)
		return nil
	}
	// Only act on left-click release — pressing-and-dragging shouldn't switch
	// channels mid-drag.
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionRelease {
		return nil
	}
	sidebarW := components.ChannelList{}.Width()
	if msg.X >= sidebarW {
		return nil
	}
	// Channel rows start at row 2 (header at 0, blank at 1). Slot = y - 2.
	slot := msg.Y - 2
	if slot < 0 || slot >= len(m.joined) {
		return nil
	}
	target := m.joined[slot]
	if target.ID == m.active.ID {
		return nil
	}
	return m.switchChannel(target.Name)
}

// altDigitSlot maps a bubbletea key string ("alt+1"…"alt+9", "alt+0") to the
// zero-based channel sidebar slot. Returns (-1, false) on miss.
func altDigitSlot(s string) (int, bool) {
	switch s {
	case "alt+1":
		return 0, true
	case "alt+2":
		return 1, true
	case "alt+3":
		return 2, true
	case "alt+4":
		return 3, true
	case "alt+5":
		return 4, true
	case "alt+6":
		return 5, true
	case "alt+7":
		return 6, true
	case "alt+8":
		return 7, true
	case "alt+9":
		return 8, true
	case "alt+0":
		return 9, true
	}
	return -1, false
}

// renderInputPreview paints a one-line styled echo of the textinput buffer
// above the input box — slash commands light up cyan, @mentions yellow,
// #channels accent, /me italic. Empty input renders an empty row so the
// chat layout's geometry stays stable. Width-budget capped so a single
// stray newline doesn't wrap.
func (m *Chat) renderInputPreview(maxWidth int) string {
	raw := m.input.Value()
	if raw == "" {
		return ""
	}
	if maxWidth <= 0 {
		maxWidth = 80
	}
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i]
	}
	// Slash command + body.
	if strings.HasPrefix(raw, "/me ") {
		return chatPreviewMe.Render("* @" + m.sess.Identity.Handle + " " + raw[4:])
	}
	if strings.HasPrefix(raw, "/") {
		head := raw
		var rest string
		if i := strings.IndexByte(raw, ' '); i > 0 {
			head = raw[:i]
			rest = raw[i:]
		}
		return chatPreviewCmd.Render(head) + chatPreviewBody.Render(rest)
	}
	// Plain message: highlight @mentions + #channels.
	var b strings.Builder
	for _, tok := range tokenizePreview(raw) {
		switch tok.kind {
		case tokMention:
			b.WriteString(chatPreviewMention.Render(tok.text))
		case tokChannel:
			b.WriteString(chatPreviewChannel.Render(tok.text))
		default:
			b.WriteString(chatPreviewBody.Render(tok.text))
		}
	}
	return b.String()
}

type previewToken struct {
	kind int
	text string
}

const (
	tokPlain = iota
	tokMention
	tokChannel
)

// tokenizePreview splits the buffer into a flat list of {plain, mention,
// channel} segments. A mention is "@" + isHandleChar*; a channel is "#" +
// isHandleChar*. Adjacent plain runs are merged.
func tokenizePreview(s string) []previewToken {
	var out []previewToken
	flush := func(b *strings.Builder) {
		if b.Len() == 0 {
			return
		}
		out = append(out, previewToken{kind: tokPlain, text: b.String()})
		b.Reset()
	}
	plain := strings.Builder{}
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '@' || c == '#' {
			j := i + 1
			for j < len(s) && isHandleChar(s[j]) {
				j++
			}
			if j > i+1 {
				flush(&plain)
				kind := tokMention
				if c == '#' {
					kind = tokChannel
				}
				out = append(out, previewToken{kind: kind, text: s[i:j]})
				i = j
				continue
			}
		}
		plain.WriteByte(c)
		i++
	}
	flush(&plain)
	return out
}

func isHandleChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

// sidebarItems produces the rendered sidebar list, decorating each row with
// the current unread count and (for DMs) the partner's presence dot.
func (m *Chat) sidebarItems() []components.ChannelListItem {
	out := make([]components.ChannelListItem, 0, len(m.joined))
	for _, c := range m.joined {
		item := components.ChannelListItem{
			ID:     c.ID,
			Name:   c.Name,
			Unread: m.unread[c.ID],
		}
		if other, ok := dmPartner(c.Name, m.sess.Identity.Handle); ok {
			item.Display = "@" + other
			item.ShowPresence = true
			item.Online = m.onlinePartners[strings.ToLower(other)]
		}
		out = append(out, item)
	}
	return out
}

//
// utility
//

func derefTopic(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// dmPartner inspects a channel name; if it's a DM channel ("dm-<lo>-<hi>") and
// self is one of the two participants, returns the OTHER participant's handle.
// Otherwise (regular channel, or self isn't a participant) returns "", false.
func dmPartner(channelName, selfHandle string) (string, bool) {
	if !strings.HasPrefix(channelName, "dm-") {
		return "", false
	}
	body := channelName[len("dm-"):]
	i := strings.Index(body, "-")
	if i < 0 {
		return "", false
	}
	a, b := body[:i], body[i+1:]
	switch strings.ToLower(selfHandle) {
	case a:
		return b, true
	case b:
		return a, true
	}
	return "", false
}
