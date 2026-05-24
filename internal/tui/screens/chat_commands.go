package screens

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/chat"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
)

// chatDMReadyMsg lands once ResolveDM has created+joined the DM channel; the
// Update loop then refans the subscription and sets the new DM active.
type chatDMReadyMsg struct{ channelName string }

// runCommand is the parsed-slash-command dispatcher. The chat package's Parse
// turns raw user input into a Command; Dispatch turns that into a typed Action,
// which we route to one of the run*/send*/edit*/openDM helpers below. Adding a
// new /command means: a Parse rule + a Dispatch arm + a new branch here.
func (m *Chat) runCommand(c chat.Command) tea.Cmd {
	switch a := chat.Dispatch(c).(type) {
	case chat.Send:
		return m.sendChat(a.Body)
	case chat.Switch:
		if a.EnsureJoin {
			return m.joinChannel(a.Channel)
		}
		return m.switchChannel(a.Channel)
	case chat.Leave:
		return m.leaveCurrent()
	case chat.Navigate:
		if m.subCancel != nil {
			m.subCancel()
		}
		return nav.Navigate(a.Target)
	case chat.Notice:
		return m.notice(a.Text)
	case chat.EditLast:
		return m.editLast(a.Body)
	case chat.OpenDM:
		return m.openDM(a.Handle)
	case chat.Who:
		return m.runWho()
	case chat.Reply:
		return m.sendReply(a.Body)
	case chat.React:
		return m.runReact(a.Emoji, true)
	case chat.Unreact:
		return m.runReact(a.Emoji, false)
	case chat.Thread:
		// Apply the filter on the active log; the View() pass will start
		// rendering the filtered set on the next frame.
		m.activeLog().SetThreadFilter(a.RootID)
		if a.RootID == 0 {
			return m.notice("thread filter cleared.")
		}
		return m.notice(fmt.Sprintf("filtered to thread #%d — /thread off to clear", a.RootID))
	case chat.DeleteLast:
		return m.runDeleteLast()
	case chat.Pin:
		return m.runSetPinLatest(true)
	case chat.Unpin:
		return m.runSetPinLatest(false)
	case chat.ListPins:
		return m.runListPins()
	case chat.Topic:
		return m.runSetTopic(a.Body)
	case chat.Search:
		return m.runSearch(a.Term)
	case chat.Finger:
		return m.runFinger(a.Handle)
	case chat.Unknown:
		return m.notice(fmt.Sprintf("unknown command /%s — try /help", a.Name))
	}
	return nil
}

// runDeleteLast tombstones the user's most recent message in the active
// channel. Mirrors the editLast lookup path so the muscle memory of the two
// commands matches: act on your own latest message in the current channel.
func (m *Chat) runDeleteLast() tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		parentID, err := m.chat.LastOwnMessageID(ctx, channelID, author.UserID)
		if err != nil {
			return chatErrMsg{stage: "find own last", err: err}
		}
		if parentID == 0 {
			return chatNoticeMsg{text: "nothing to delete — you haven't posted in this channel yet"}
		}
		if err := m.chat.DeleteMessage(ctx, parentID, author); err != nil {
			var forbidden realtime.ErrForbidden
			if errors.As(err, &forbidden) {
				return chatNoticeMsg{text: forbidden.Error()}
			}
			return chatErrMsg{stage: "delete", err: err}
		}
		return nil
	}
}

// runSetPinLatest pin/unpins the channel's most-recent visible message. .NET
// keys /pin off a position number; we use "latest" so the UX is one keystroke
// and matches /react's authoring shape.
func (m *Chat) runSetPinLatest(pin bool) tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		msgID, err := m.chat.LatestMessageID(ctx, channelID)
		if err != nil {
			return chatErrMsg{stage: "find latest", err: err}
		}
		if msgID == 0 {
			return chatNoticeMsg{text: "no message in this channel to pin"}
		}
		if err := m.chat.SetPinned(ctx, msgID, pin, author); err != nil {
			var forbidden realtime.ErrForbidden
			if errors.As(err, &forbidden) {
				return chatNoticeMsg{text: forbidden.Error()}
			}
			return chatErrMsg{stage: "pin", err: err}
		}
		return nil
	}
}

// runListPins renders the channel's pinned messages as a series of notice
// lines, ordered newest-pinned first. Long bodies are truncated to 80 chars
// to keep the pin list legible.
func (m *Chat) runListPins() tea.Cmd {
	channelID := m.active.ID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		pins, err := m.chat.ListPins(ctx, channelID)
		if err != nil {
			return chatErrMsg{stage: "list pins", err: err}
		}
		if len(pins) == 0 {
			return chatNoticeMsg{text: "no pinned messages in this channel."}
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("── pinned (%d) ──", len(pins)))
		for _, p := range pins {
			preview := p.Body
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			b.WriteString("\n  ★ ")
			b.WriteString(m.sess.DisplayPrefs.FormatClock(p.CreatedAt))
			b.WriteString(" @")
			b.WriteString(p.Handle)
			b.WriteString(": ")
			b.WriteString(preview)
		}
		return chatNoticeMsg{text: b.String()}
	}
}

// runSetTopic publishes a new channel topic. Empty body clears it. The chat
// screen's status row picks up the new topic via the bus subscription, so we
// don't need to mutate m.active.Topic locally — the event handler does that.
func (m *Chat) runSetTopic(body string) tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	var topicPtr *string
	if body != "" {
		t := body
		topicPtr = &t
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if err := m.chat.SetTopic(ctx, channelID, topicPtr, author); err != nil {
			var forbidden realtime.ErrForbidden
			if errors.As(err, &forbidden) {
				return chatNoticeMsg{text: forbidden.Error()}
			}
			return chatErrMsg{stage: "set topic", err: err}
		}
		return nil
	}
}

// runSearch performs a tsvector-backed search and renders matches as a
// notice, oldest-match first so the visual reads top-to-bottom chronologically.
func (m *Chat) runSearch(term string) tea.Cmd {
	channelID := m.active.ID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		hits, err := m.chat.Search(ctx, channelID, term, 50)
		if err != nil {
			return chatErrMsg{stage: "search", err: err}
		}
		if len(hits) == 0 {
			return chatNoticeMsg{text: fmt.Sprintf("no matches for %q.", term)}
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("── search %q — %d match", term, len(hits)))
		if len(hits) != 1 {
			b.WriteString("es")
		}
		b.WriteString(" ──")
		// Reverse to oldest-first so the visual reads top-to-bottom in time
		// order, like a normal log scroll.
		for i := len(hits) - 1; i >= 0; i-- {
			h := hits[i]
			preview := h.Body
			if len(preview) > 120 {
				preview = preview[:120] + "…"
			}
			b.WriteString("\n  ")
			b.WriteString(m.sess.DisplayPrefs.FormatClock(h.CreatedAt))
			b.WriteString(" @")
			b.WriteString(h.Handle)
			b.WriteString(": ")
			b.WriteString(preview)
		}
		return chatNoticeMsg{text: b.String()}
	}
}

// runFinger navigates to the Profile screen's read-only Finger viewer for
// `handle`. The Profile screen handles "no such user" inside its own View,
// so we don't pre-validate here. When the ProfileService is missing (e.g.
// in tests) we degrade to a chat notice rather than navigate.
func (m *Chat) runFinger(handle string) tea.Cmd {
	if m.sess.Profile == nil {
		return m.notice("profile service not configured")
	}
	return nav.NavigateWith(nav.DestProfile, strings.TrimPrefix(handle, "@"))
}

// runWho fetches the current online list from PresenceService and renders it
// as a Notice in the active log. Excludes self because the user already knows
// they're online; if no one else is online, says so explicitly.
func (m *Chat) runWho() tea.Cmd {
	if m.sess.Presence == nil {
		return m.notice("presence service not configured")
	}
	selfHandle := strings.ToLower(m.sess.Identity.Handle)
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(3 * time.Second)
		defer cancel()
		handles, err := m.sess.Presence.OnlineHandles(ctx)
		if err != nil {
			return chatErrMsg{stage: "who", err: err}
		}
		others := make([]string, 0, len(handles))
		for _, h := range handles {
			if strings.EqualFold(h, selfHandle) {
				continue
			}
			others = append(others, "@"+h)
		}
		if len(others) == 0 {
			return chatNoticeMsg{text: "you're the only one here right now"}
		}
		return chatNoticeMsg{text: "online: " + strings.Join(others, ", ")}
	}
}

// runReact adds (add=true) or removes (add=false) an emoji reaction on the
// channel's most recent visible message. We use LatestMessageID (not own-last)
// because reacting to others' messages is the common case.
func (m *Chat) runReact(emoji string, add bool) tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		msgID, err := m.chat.LatestMessageID(ctx, channelID)
		if err != nil {
			return chatErrMsg{stage: "find latest", err: err}
		}
		if msgID == 0 {
			return chatNoticeMsg{text: "no message in this channel to react to"}
		}
		if add {
			if err := m.chat.React(ctx, channelID, msgID, author, emoji); err != nil {
				return chatErrMsg{stage: "react", err: err}
			}
		} else {
			if err := m.chat.Unreact(ctx, channelID, msgID, author, emoji); err != nil {
				return chatErrMsg{stage: "unreact", err: err}
			}
		}
		return nil
	}
}

// sendReply looks up the user's most-recent message in the active channel
// and writes a reply with parent_message_id set. Surfaces a notice if the
// user has no message to thread under.
func (m *Chat) sendReply(body string) tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		parentID, err := m.chat.LastOwnMessageID(ctx, channelID, author.UserID)
		if err != nil {
			return chatErrMsg{stage: "find parent", err: err}
		}
		if parentID == 0 {
			return chatNoticeMsg{text: "no message to reply to — send something first"}
		}
		if _, err := m.chat.SendReply(ctx, channelID, author, body, &parentID); err != nil {
			return chatErrMsg{stage: "reply", err: err}
		}
		return nil
	}
}

func (m *Chat) sendChat(body string) tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if _, err := m.chat.Send(ctx, channelID, author, body); err != nil {
			return chatErrMsg{stage: "send", err: err}
		}
		return nil
	}
}

func (m *Chat) editLast(newBody string) tea.Cmd {
	channelID := m.active.ID
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		_, err := m.chat.EditLastOwnInChannel(ctx, channelID, author, newBody)
		if err != nil {
			if errors.Is(err, realtime.ErrNoMessageToEdit) {
				return chatNoticeMsg{text: "nothing to edit — you haven't posted in this channel yet"}
			}
			return chatErrMsg{stage: "edit", err: err}
		}
		return nil
	}
}

func (m *Chat) openDM(handle string) tea.Cmd {
	self := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		ch, err := m.chat.ResolveDM(ctx, self, handle)
		if err != nil {
			switch {
			case errors.Is(err, realtime.ErrUnknownHandle):
				return chatNoticeMsg{text: fmt.Sprintf("no user named @%s", handle)}
			case errors.Is(err, realtime.ErrCannotDMSelf):
				return chatNoticeMsg{text: "can't DM yourself"}
			default:
				return chatErrMsg{stage: "open dm", err: err}
			}
		}
		// ResolveDM creates+memberships; refanout pulls the new DM channel
		// into the merged sub and sets it active.
		return chatDMReadyMsg{channelName: ch.Name}
	}
}
