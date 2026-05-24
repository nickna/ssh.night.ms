package screens

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickna/ssh.night.ms/internal/realtime"
)

// typing constants — kept in one place so the publish throttle and the
// receive TTL match. typingTTL must be larger than the publish interval so
// a typist's indicator doesn't flicker between back-to-back keystrokes.
const (
	typingPublishInterval = 1500 * time.Millisecond
	typingTTL             = 4 * time.Second
)

// typingTickMsg fires every second so the screen can prune expired entries
// even when no new keys are flowing. Without this the indicator would
// linger past TTL whenever the typist stops without a clean signal.
type typingTickMsg time.Time

// scheduleTypingTick re-fires every second so expired typing entries clear
// out even when no new keystrokes are landing. The handler reschedules.
func (m *Chat) scheduleTypingTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return typingTickMsg(t) })
}

// publishTyping fires the bus publish in a tea.Cmd so the Update loop never
// blocks on Redis. Failure is logged but non-fatal — losing a typing
// indicator is harmless.
func (m *Chat) publishTyping(channelID int64) tea.Cmd {
	author := realtime.Author{
		UserID:  m.sess.Identity.UserID,
		Handle:  m.sess.Identity.Handle,
		IsSysop: m.sess.Identity.IsSysop,
	}
	svc := m.chat
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(2 * time.Second)
		defer cancel()
		if err := svc.PublishTyping(ctx, channelID, author); err != nil {
			m.sess.Logger.Warn("chat: publish typing", "err", err)
		}
		return nil
	}
}

// typingFooter renders "X is typing", "X and Y are typing", or
// "N people are typing" for the active channel. Self is already filtered out
// at receive time; here we only worry about expiry (Update prunes hourly,
// but the View runs more often, so do one more pass).
func (m *Chat) typingFooter() string {
	perChannel := m.typing[m.active.ID]
	if len(perChannel) == 0 {
		return ""
	}
	now := time.Now()
	var live []string
	for handle, exp := range perChannel {
		if now.Before(exp) {
			live = append(live, handle)
		}
	}
	switch len(live) {
	case 0:
		return ""
	case 1:
		return live[0] + " is typing…"
	case 2:
		return live[0] + " and " + live[1] + " are typing…"
	default:
		return fmt.Sprintf("%d people are typing…", len(live))
	}
}
