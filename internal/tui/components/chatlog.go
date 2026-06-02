package components

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/chat"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// ChatLog is the scrolling chat surface used by the Chat screen. Append-
// only with width-driven word wrap, a stick-to-bottom scroll model, and an
// indexByID map so edits/deletes patch in place without re-rendering the
// whole log. Capped at 5,000 entries.
type ChatLog struct {
	entries      []logEntry
	byID         map[int64]int
	width        int
	height       int
	scrollOffset int // 0 = bottom (latest); positive = lines from bottom

	// selfHandle drives @mention coloring (self vs. other) and the bool
	// returned from Append so the chat screen can flash a status badge when
	// the user is mentioned. Set via SetSelfHandle once on bootstrap.
	selfHandle string

	// Reactions overlay: per-message emoji counts. Mutated by SetReactions
	// (bootstrap snapshot) and AddReaction/RemoveReaction (live events). The
	// chip line is rendered as the last line of an entry whenever a message
	// has at least one reaction.
	reactions map[int64]map[string]int

	// pfpByHandle keys lowercased handles → "has uploaded a profile picture
	// at some point." Primed by SetPfpHandles on bootstrap so the "●" marker
	// is correct on first paint instead of trickling in across N background
	// lookups. Lazy lookup for handles missing from the map is the chat
	// screen's responsibility (it calls SetPfp once it learns the value).
	pfpByHandle map[string]bool

	// threadFilter, when non-zero, hides entries that aren't either the
	// root (id == threadFilter) or descended from it (parent_message_id ==
	// threadFilter). Drives the /thread N view from the Chat screen.
	threadFilter int64

	// timeFormatter renders the "[HH:MM]" message-header timestamp. The
	// chat screen wires this to the session-cached DisplayPrefs so the
	// log respects the user's clock format. nil falls back to plain
	// server-local 24-hour output — used by tests + screens that haven't
	// wired one up.
	timeFormatter func(time.Time) string
}

// SetThreadFilter pins the log to a single thread; pass 0 to clear and show
// the whole channel again. Snaps to bottom so the newest reply is visible.
func (c *ChatLog) SetThreadFilter(rootID int64) {
	c.threadFilter = rootID
	c.scrollOffset = 0
}

// ThreadFilter returns the current filter root (0 = no filter). Callers use
// it to decide whether to show "thread mode" affordances in the header.
func (c *ChatLog) ThreadFilter() int64 { return c.threadFilter }

// SetTimeFormatter installs a callback the log uses to render message
// timestamps. Re-wraps all entries so existing history picks up the new
// format on the next paint. Pass nil to revert to the default
// server-local 24-hour fallback.
func (c *ChatLog) SetTimeFormatter(fn func(time.Time) string) {
	c.timeFormatter = fn
	for i := range c.entries {
		c.rewrapAt(i)
	}
}

// formatTimestamp returns the header "[HH:MM]" form, routed through the
// installed formatter when present, otherwise a server-local 24-hour
// fallback. Centralized so callers can't accidentally bypass the
// formatter.
func (c *ChatLog) formatTimestamp(t time.Time) string {
	if c.timeFormatter != nil {
		return c.timeFormatter(t)
	}
	return t.Local().Format("15:04")
}

// SetSelfHandle remembers the viewer's handle so message rendering can colorize
// @mentions of self differently and so Append can report when a message
// mentions the viewer (used by the chat screen to flash a status badge).
// Calling this re-wraps all entries so existing history picks up the new
// mention coloring on the next View().
func (c *ChatLog) SetSelfHandle(h string) {
	if c.selfHandle == h {
		return
	}
	c.selfHandle = h
	for i := range c.entries {
		c.rewrapAt(i)
	}
}

// rewrapAt regenerates the cached wrapped form of one entry. Knows about the
// system-line variant so callers don't have to branch.
func (c *ChatLog) rewrapAt(i int) {
	if c.entries[i].system {
		c.entries[i].wrapped = c.wrapSystemLines(c.entries[i].systemText)
		return
	}
	c.entries[i].wrapped, c.entries[i].selfMentioned = c.renderEntry(c.entries[i].msg)
}

const chatLogMaxEntries = 5000

type logEntry struct {
	msg     realtime.Message
	wrapped []string // pre-wrapped lines including header (recomputed on width change)
	// selfMentioned is true when the body @-mentions selfHandle. Surfaced by
	// Append so the screen can warn ("@ you were mentioned"). Recomputed on
	// every render so an edit that drops a mention clears the flag.
	selfMentioned bool
	// replyCount is the number of direct children of this message. Mutated by
	// SetReplyCounts (bootstrap snapshot) and Append (live bump when a reply
	// lands). Rendered as a trailing "  [N replies]" badge on parents.
	replyCount int
	// imageLines is the rendered half-block ANSI for any inline image
	// referenced from the body. Set by AttachImage after the fetcher
	// completes; appended after body lines so the picture sits below the
	// text. Multiple images concatenate.
	imageLines []string
	// system is set when this entry isn't a real chat message but a screen-
	// generated notice (/help, /pins, /search, etc.). System entries skip
	// timestamp + handle chrome and render as a styled cyan info line.
	system bool
	// systemText is the raw text for a system entry, retained across width
	// changes so SetSize can rewrap without losing the source.
	systemText string
}

// NewChatLog allocates a fresh log. Width is supplied later via SetSize.
func NewChatLog() *ChatLog {
	return &ChatLog{
		byID:        make(map[int64]int),
		reactions:   make(map[int64]map[string]int),
		pfpByHandle: make(map[string]bool),
	}
}

// SetPfpHandles primes the "● user has a profile picture" overlay from a
// bulk snapshot. Keys are lowercased handles. Triggers a rewrap of affected
// entries so the marker appears on the next View().
func (c *ChatLog) SetPfpHandles(snapshot map[string]bool) {
	for k, v := range snapshot {
		c.pfpByHandle[strings.ToLower(k)] = v
	}
	// Re-render every entry whose author is in the snapshot — cheap because
	// re-render is just per-message wrap.
	for i := range c.entries {
		if _, ok := snapshot[strings.ToLower(c.entries[i].msg.Handle)]; ok {
			c.entries[i].wrapped, c.entries[i].selfMentioned = c.renderEntry(c.entries[i].msg)
		}
	}
}

// SetPfp records a single handle's pfp state. Cheaper than the bulk path for
// the chat screen's lazy fill-in: a live message from a previously-unseen
// handle queries ProfileService once and calls this with the result. No-op
// when the value matches the cache.
func (c *ChatLog) SetPfp(handle string, has bool) {
	key := strings.ToLower(handle)
	if cur, ok := c.pfpByHandle[key]; ok && cur == has {
		return
	}
	c.pfpByHandle[key] = has
	for i := range c.entries {
		if strings.EqualFold(c.entries[i].msg.Handle, handle) {
			c.entries[i].wrapped, c.entries[i].selfMentioned = c.renderEntry(c.entries[i].msg)
		}
	}
}

// HasPfp answers "is this handle known to have an uploaded profile picture?"
// Returns (false, false) when the handle hasn't been primed yet so the chat
// screen can decide to kick off a lookup.
func (c *ChatLog) HasPfp(handle string) (has, known bool) {
	v, ok := c.pfpByHandle[strings.ToLower(handle)]
	return v, ok
}

// SetReplyCounts replaces the per-parent reply-count overlay with a fresh
// snapshot (typical caller: chat screen on bootstrap). Forces a rewrap of
// any entry whose count just changed so the "[N replies]" badge appears.
func (c *ChatLog) SetReplyCounts(snapshot map[int64]int) {
	for parentID, n := range snapshot {
		idx, ok := c.byID[parentID]
		if !ok {
			continue
		}
		if c.entries[idx].replyCount == n {
			continue
		}
		c.entries[idx].replyCount = n
		c.entries[idx].wrapped, c.entries[idx].selfMentioned = c.renderEntry(c.entries[idx].msg)
	}
}

// MarkDeleted flags the given message as deleted and rewraps its entry so the
// tombstone renders. Also drops any attached image so a deleted message
// doesn't leave its picture on screen. No-op for unknown ids.
func (c *ChatLog) MarkDeleted(messageID int64, when time.Time) {
	idx, ok := c.byID[messageID]
	if !ok {
		return
	}
	if !c.entries[idx].msg.DeletedAt.IsZero() {
		return
	}
	c.entries[idx].msg.DeletedAt = when
	c.entries[idx].imageLines = nil
	c.entries[idx].wrapped, c.entries[idx].selfMentioned = c.renderEntry(c.entries[idx].msg)
}

// MarkPinned flips an entry's is_pinned state in place and rewraps. No-op
// when the value matches or the id isn't loaded.
func (c *ChatLog) MarkPinned(messageID int64, pinned bool) {
	idx, ok := c.byID[messageID]
	if !ok {
		return
	}
	if c.entries[idx].msg.IsPinned == pinned {
		return
	}
	c.entries[idx].msg.IsPinned = pinned
	c.entries[idx].wrapped, c.entries[idx].selfMentioned = c.renderEntry(c.entries[idx].msg)
}

// AttachImage appends pre-rendered half-block lines to the named message so
// the inline image paints below the body on the next View(). No-op when the
// id has scrolled past the load cap. Multiple calls concatenate (one message
// can carry several image URLs).
func (c *ChatLog) AttachImage(messageID int64, lines []string) {
	if len(lines) == 0 {
		return
	}
	idx, ok := c.byID[messageID]
	if !ok {
		return
	}
	c.entries[idx].imageLines = append(c.entries[idx].imageLines, lines...)
}

// ClearImages drops the cached image lines for a message. Used when the
// message is tombstoned so the picture doesn't outlive its caption.
func (c *ChatLog) ClearImages(messageID int64) {
	idx, ok := c.byID[messageID]
	if !ok {
		return
	}
	c.entries[idx].imageLines = nil
}

// SetReactions replaces the reaction overlay with a fresh snapshot — typical
// caller is the chat screen on bootstrap / channel switch. Forces a rewrap of
// affected entries so the chip line appears on the next View().
func (c *ChatLog) SetReactions(snapshot map[int64]map[string]int) {
	c.reactions = map[int64]map[string]int{}
	for k, v := range snapshot {
		cp := make(map[string]int, len(v))
		for e, n := range v {
			cp[e] = n
		}
		c.reactions[k] = cp
	}
	for id := range c.reactions {
		c.rewrapByID(id)
	}
}

// AddReaction increments the count for (msgID, emoji). Idempotent on the wire
// (the SQL is INSERT ON CONFLICT DO NOTHING) but here we increment uncritically
// because the event we receive should correspond to a real insert.
func (c *ChatLog) AddReaction(msgID int64, emoji string) {
	if c.reactions[msgID] == nil {
		c.reactions[msgID] = map[string]int{}
	}
	c.reactions[msgID][emoji]++
	c.rewrapByID(msgID)
}

// RemoveReaction decrements (and deletes the key when it drops to zero so the
// chip line collapses cleanly).
func (c *ChatLog) RemoveReaction(msgID int64, emoji string) {
	m, ok := c.reactions[msgID]
	if !ok {
		return
	}
	m[emoji]--
	if m[emoji] <= 0 {
		delete(m, emoji)
	}
	if len(m) == 0 {
		delete(c.reactions, msgID)
	}
	c.rewrapByID(msgID)
}

// rewrapByID re-renders the entry for the given message ID. No-op if the
// entry isn't in the log (e.g., a reaction for a message older than our
// 5K-entry cap window).
func (c *ChatLog) rewrapByID(msgID int64) {
	i, ok := c.byID[msgID]
	if !ok {
		return
	}
	c.entries[i].wrapped, c.entries[i].selfMentioned = c.renderEntry(c.entries[i].msg)
}

// SetSize re-flows the visible window to the new dimensions. If the width
// changed every entry is re-wrapped (the alternative is a per-line wrap
// cache that doesn't pay for itself at chat-message rate).
func (c *ChatLog) SetSize(width, height int) {
	if width == c.width && height == c.height {
		return
	}
	c.height = height
	if width == c.width {
		return
	}
	c.width = width
	for i := range c.entries {
		c.rewrapAt(i)
	}
}

// Append adds a message to the log. If a message with the same ID already
// exists (replay during reconnect, or future edit events) we update in place,
// preserving the cached replyCount so an edit doesn't drop the badge.
// Capacity beyond the cap evicts oldest. Returns true when the rendered body
// mentions selfHandle (set via SetSelfHandle) so the parent screen can
// surface a notification.
func (c *ChatLog) Append(m realtime.Message) bool {
	wrapped, mentioned := c.renderEntry(m)
	if i, ok := c.byID[m.ID]; ok && m.ID != 0 {
		existing := c.entries[i]
		c.entries[i] = logEntry{
			msg:           m,
			wrapped:       wrapped,
			selfMentioned: mentioned,
			replyCount:    existing.replyCount, // preserve across edit
		}
		// If we just learned this entry's reply count via SetReplyCounts and
		// then the message re-arrived (rare), keep the wrapped form aligned
		// with the preserved count.
		if existing.replyCount > 0 {
			c.entries[i].wrapped, c.entries[i].selfMentioned = c.renderEntry(m)
		}
		c.bumpParentReplyCount(m)
		return mentioned
	}
	c.entries = append(c.entries, logEntry{msg: m, wrapped: wrapped, selfMentioned: mentioned})
	if m.ID != 0 {
		c.byID[m.ID] = len(c.entries) - 1
	}
	c.bumpParentReplyCount(m)
	if len(c.entries) > chatLogMaxEntries {
		drop := len(c.entries) - chatLogMaxEntries
		for _, e := range c.entries[:drop] {
			delete(c.byID, e.msg.ID)
		}
		c.entries = c.entries[drop:]
		// Reindex — slice indices shifted by `drop`.
		for i, e := range c.entries {
			if e.msg.ID != 0 {
				c.byID[e.msg.ID] = i
			}
		}
	}
	return mentioned
}

// bumpParentReplyCount increments the reply-count badge on the parent if it's
// still in the on-screen window. Re-wraps the parent so the badge updates.
// No-op when the parent has scrolled off — SetReplyCounts on next history
// load will reconcile.
func (c *ChatLog) bumpParentReplyCount(m realtime.Message) {
	if m.ParentMessageID == nil {
		return
	}
	pid := *m.ParentMessageID
	idx, ok := c.byID[pid]
	if !ok {
		return
	}
	c.entries[idx].replyCount++
	c.entries[idx].wrapped, c.entries[idx].selfMentioned = c.renderEntry(c.entries[idx].msg)
}

// AppendAll seeds the log from a history slice. Useful on screen mount.
// Self-mention bubble-ups are ignored for history (it's old; the user can see
// the highlight in scrollback).
func (c *ChatLog) AppendAll(msgs []realtime.Message) {
	for _, m := range msgs {
		c.Append(m)
	}
}

// AppendSystem appends a screen-generated notice — /help output, /pins
// listings, error hints — as a clean info-styled block. Each `\n`-separated
// input line word-wraps independently so a long sentence flows; the chat
// log never paints a timestamp or "@*" handle for these.
func (c *ChatLog) AppendSystem(text string) {
	wrapped := c.wrapSystemLines(text)
	c.entries = append(c.entries, logEntry{
		wrapped:    wrapped,
		system:     true,
		systemText: text,
	})
	if len(c.entries) > chatLogMaxEntries {
		drop := len(c.entries) - chatLogMaxEntries
		for _, e := range c.entries[:drop] {
			delete(c.byID, e.msg.ID)
		}
		c.entries = c.entries[drop:]
		for i, e := range c.entries {
			if e.msg.ID != 0 {
				c.byID[e.msg.ID] = i
			}
		}
	}
}

// ScrollUp shifts the window up by N lines (pinning at the top).
func (c *ChatLog) ScrollUp(n int) {
	total := c.totalLines()
	c.scrollOffset += n
	max := total - c.height
	if max < 0 {
		max = 0
	}
	if c.scrollOffset > max {
		c.scrollOffset = max
	}
}

// ScrollDown shifts the window down by N lines (snapping to bottom at 0).
func (c *ChatLog) ScrollDown(n int) {
	c.scrollOffset -= n
	if c.scrollOffset < 0 {
		c.scrollOffset = 0
	}
}

// SnapToBottom resets scroll position. Called after sending a message so the
// user sees their own line.
func (c *ChatLog) SnapToBottom() { c.scrollOffset = 0 }

// View paints the visible window of `height` lines. The latest content is
// always at the bottom of the box; older content scrolls up off the top.
func (c *ChatLog) View() string {
	if c.height <= 0 || c.width <= 0 {
		return ""
	}
	all := c.flatten()
	end := len(all) - c.scrollOffset
	if end > len(all) {
		end = len(all)
	}
	start := end - c.height
	if start < 0 {
		start = 0
	}
	visible := all[start:end]

	// Pad with empty lines at the top so the log stays anchored to the bottom
	// when it has fewer than `height` lines of content.
	if pad := c.height - len(visible); pad > 0 {
		filler := make([]string, pad)
		visible = append(filler, visible...)
	}
	return strings.Join(visible, "\n")
}

func (c *ChatLog) flatten() []string {
	total := 0
	for _, e := range c.entries {
		if !c.entryInFilter(e.msg) {
			continue
		}
		total += len(e.wrapped) + len(e.imageLines)
	}
	out := make([]string, 0, total)
	for _, e := range c.entries {
		if !c.entryInFilter(e.msg) {
			continue
		}
		out = append(out, e.wrapped...)
		if len(e.imageLines) > 0 {
			out = append(out, e.imageLines...)
		}
	}
	return out
}

// entryInFilter returns true when m should be rendered under the current
// threadFilter setting (always true when no filter is active).
func (c *ChatLog) entryInFilter(m realtime.Message) bool {
	if c.threadFilter == 0 {
		return true
	}
	if m.ID == c.threadFilter {
		return true
	}
	return m.ParentMessageID != nil && *m.ParentMessageID == c.threadFilter
}

func (c *ChatLog) totalLines() int {
	n := 0
	for _, e := range c.entries {
		if !c.entryInFilter(e.msg) {
			continue
		}
		n += len(e.wrapped) + len(e.imageLines)
	}
	return n
}

// findParentHandle returns the handle of the parent message if it's still in
// the on-screen window (the byID cache), or "" if it's scrolled off. Used to
// render the "↳ @parent" reply prefix.
func (c *ChatLog) findParentHandle(parentID int64) string {
	i, ok := c.byID[parentID]
	if !ok {
		return ""
	}
	return c.entries[i].msg.Handle
}

var (
	chatTimeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	chatSysopStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	chatEditedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	chatDeletedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	// System info lines — /help output, /pins listings, error hints.
	chatSystemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	// Reply chrome: dim "↳ @" + colored parent handle.
	chatReplyChrome   = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	chatReplyMention  = lipgloss.NewStyle().Foreground(lipgloss.Color(chat.ColorMentionOther))
	chatReactionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	chatReactionCount = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted))
	// ★ pinned marker. Bright orange-gold from ChatPalette.Pin.
	chatPinStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFC84C"))
	// [N replies] suffix. Italic muted to match (edited) chrome.
	chatReplyBadgeStyle = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color(theme.ColorMuted))
)

// renderEntry produces the wrapped representation for one message + a bool
// signalling whether the body mentions selfHandle. Returns at least one line
// (the header). Body lines are indented by 2 spaces so they align under the
// handle. A non-zero EditedAt appends a small "(edited)" marker to the last
// body line; a non-zero DeletedAt overrides the body with a "(deleted)"
// tombstone (chrome + chip line stay so scrollback context is preserved).
//
// /me actions (body starts with chat.MeMarker) render as a single italic line
// "[ts] * @handle <action>" with no body indent — same vertical density as a
// regular message but visually distinct.
//
// Reply count badges are appended as a trailing "  [N replies]" italic chrome
// span on the last body line; the chat screen maintains the count via
// SetReplyCounts + Append's bumpParentReplyCount.
func (c *ChatLog) renderEntry(m realtime.Message) ([]string, bool) {
	ts := c.formatTimestamp(m.CreatedAt)

	handleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(handleColorFor(m.Handle)))

	// Deleted tombstone — chrome stays so reactions / context anchors don't
	// move; the body becomes "(deleted)" in faint italic. No body wrap, no
	// reactions/badges (they don't apply to a tombstone).
	if !m.DeletedAt.IsZero() {
		header := chatTimeStyle.Render("["+ts+"]") + " " + handleStyle.Render("@"+m.Handle) + chatTimeStyle.Render(":")
		header += " " + chatDeletedStyle.Render("(deleted)")
		return []string{header}, false
	}

	// /me emote — one italic line carrying the action body, no body indent.
	if strings.HasPrefix(m.Body, chat.MeMarker) {
		action := strings.TrimPrefix(m.Body, chat.MeMarker)
		bodyWidth := c.width - 2
		if bodyWidth < 8 {
			bodyWidth = 8
		}
		emoteStyle := lipgloss.NewStyle().
			Italic(true).
			Bold(true).
			Foreground(lipgloss.Color(handleColorFor(m.Handle)))
		actionLines, mentioned := chat.WrapBodyLines(action, c.selfHandle, bodyWidth)
		out := make([]string, 0, len(actionLines))
		for i, bl := range actionLines {
			head := chatTimeStyle.Render("["+ts+"]") + " " + emoteStyle.Render("* @"+m.Handle+" ")
			if i == 0 {
				line := head + bl
				if i == len(actionLines)-1 && !m.EditedAt.IsZero() {
					line += " " + chatEditedStyle.Render("(edited)")
				}
				out = append(out, line)
			} else {
				line := "  " + bl
				if i == len(actionLines)-1 && !m.EditedAt.IsZero() {
					line += " " + chatEditedStyle.Render("(edited)")
				}
				out = append(out, line)
			}
		}
		return out, mentioned
	}

	// Header: "[★] [ts] [●] @handle [SYSOP]:" then optional "↳ @parent"
	// reply prefix. The pin star always leads so it's visible regardless of
	// whether the message is otherwise decorated; the PFP dot is inline with
	// the handle so the eye reads them together as one identity badge.
	var header string
	if m.IsPinned {
		header += chatPinStyle.Render("★") + " "
	}
	header += chatTimeStyle.Render("["+ts+"]") + " "
	if has, _ := c.HasPfp(m.Handle); has {
		// Color the dot in the sender's stable color so the eye associates
		// it with the handle that follows.
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(handleColorFor(m.Handle))).Render("●")
		header += dot + " "
	}
	header += handleStyle.Render("@" + m.Handle)
	if m.IsSysop {
		header += " " + chatSysopStyle.Render("SYSOP")
	}
	header += chatTimeStyle.Render(":")

	if m.ParentMessageID != nil {
		parent := c.findParentHandle(*m.ParentMessageID)
		if parent == "" {
			parent = "(earlier)"
		}
		header += " " + chatReplyChrome.Render("↳ @") + chatReplyMention.Render(parent)
	}

	bodyWidth := c.width - 2
	if bodyWidth < 8 {
		bodyWidth = 8
	}
	bodyLines, mentioned := chat.WrapBodyLines(m.Body, c.selfHandle, bodyWidth)
	out := make([]string, 0, 1+len(bodyLines))
	out = append(out, header)
	replyCount := 0
	if idx, ok := c.byID[m.ID]; ok {
		replyCount = c.entries[idx].replyCount
	}
	for i, bl := range bodyLines {
		rendered := "  " + bl
		last := i == len(bodyLines)-1
		if last && !m.EditedAt.IsZero() {
			rendered += " " + chatEditedStyle.Render("(edited)")
		}
		if last && replyCount > 0 {
			label := "1 reply"
			if replyCount > 1 {
				label = fmt.Sprintf("%d replies", replyCount)
			}
			rendered += "  " + chatReplyBadgeStyle.Render("["+label+"]")
		}
		out = append(out, rendered)
	}
	if line := c.reactionsLine(m.ID, "  "); line != "" {
		out = append(out, line)
	}
	return out, mentioned
}

// handleColorFor falls back to the theme accent color for empty handles so
// the "*" notice writer doesn't paint as black on black.
func handleColorFor(handle string) string {
	if c := chat.HandleColor(handle); c != "" {
		return c
	}
	return theme.ColorAccent
}

// wrapSystemLines splits `text` on \n and word-wraps each line to the chat
// log's current width, then styles each wrapped row as a system info line.
// Empty input still returns one blank line so the entry occupies a row.
func (c *ChatLog) wrapSystemLines(text string) []string {
	width := c.width
	if width <= 0 {
		width = 80
	}
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		if raw == "" {
			out = append(out, "")
			continue
		}
		lines, _ := chat.WrapBodyLines(raw, "", width)
		for _, l := range lines {
			out = append(out, chatSystemStyle.Render(stripSGR(l)))
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// stripSGR removes any pre-styled SGR escapes from a wrap line before the
// system colorizer paints over it. WrapBodyLines bakes in bold/italic/etc.
// for tokens like @mentions; system text wants a uniform info color.
func stripSGR(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// reactionsLine renders one chip per emoji as "emoji count" separated by two
// spaces, indented to match body lines. Returns "" when the message has no
// reactions so callers can skip appending.
func (c *ChatLog) reactionsLine(msgID int64, indent string) string {
	r := c.reactions[msgID]
	if len(r) == 0 {
		return ""
	}
	// Stable order: alphabetical by emoji so two clients render the same.
	keys := make([]string, 0, len(r))
	for e := range r {
		keys = append(keys, e)
	}
	sort.Strings(keys)
	chips := make([]string, 0, len(keys))
	for _, e := range keys {
		chips = append(chips, chatReactionStyle.Render(e)+" "+chatReactionCount.Render(formatReactionCount(r[e])))
	}
	return indent + strings.Join(chips, "  ")
}

// formatReactionCount caps at 99+ to keep the chip line tidy.
func formatReactionCount(n int) string {
	if n >= 100 {
		return "99+"
	}
	return fmt.Sprintf("%d", n)
}

