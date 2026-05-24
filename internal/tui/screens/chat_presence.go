package screens

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// presenceRefreshInterval is half the heartbeat TTL so the dot updates land
// inside one TTL window. Matches the convention from PresenceService defaults.
const presenceRefreshInterval = 30 * time.Second

// onlineRefreshInterval governs how often the right-rail "online (N)" list
// re-queries Redis. Shorter than presenceRefreshInterval because the roster
// is what the user reads directly — a 15s cadence keeps it close to real-time
// without hammering the keyspace scan.
const onlineRefreshInterval = 15 * time.Second

// mentionFlashDuration is how long "@ you were mentioned" lingers in the
// status row before reverting to typing/static hint.
const mentionFlashDuration = 6 * time.Second

// presenceTickMsg triggers a presence re-read.
type presenceTickMsg struct{}

// presenceOnlineMsg carries the result of OnlineMany back into Update.
type presenceOnlineMsg map[string]bool

// onlineTickMsg fires the right-rail handle-list refresh.
type onlineTickMsg struct{}

// onlineRosterMsg carries the result of the OnlineHandles scan.
type onlineRosterMsg []string

// scheduleOnlineRoster fires onlineTickMsg after onlineRefreshInterval; the
// Update handler re-queries PresenceService.OnlineHandles and reschedules.
func (m *Chat) scheduleOnlineRoster() tea.Cmd {
	return tea.Tick(onlineRefreshInterval, func(time.Time) tea.Msg { return onlineTickMsg{} })
}

// refreshOnlineRoster fetches the global online-handles list. .NET tracks
// per-channel presence via a Redis ZSET; the Go PresenceService is
// process-global today, so we surface the same list for every channel and
// let the right-rail label reflect global online count. If/when a per-channel
// presence path lands, this is the only call site that needs swapping.
func (m *Chat) refreshOnlineRoster() tea.Cmd {
	if m.sess.Presence == nil {
		return nil
	}
	presence := m.sess.Presence
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(2 * time.Second)
		defer cancel()
		handles, err := presence.OnlineHandles(ctx)
		if err != nil {
			m.sess.Logger.Warn("chat: online roster refresh", "err", err)
			return nil
		}
		return onlineRosterMsg(handles)
	}
}

// scheduleOnlineRefresh fires a presenceTick after a short delay; the Update
// handler responds by querying PresenceService.OnlineMany for DM partners
// and re-scheduling.
func (m *Chat) scheduleOnlineRefresh() tea.Cmd {
	return tea.Tick(presenceRefreshInterval, func(time.Time) tea.Msg { return presenceTickMsg{} })
}

// refreshPartnerPresence fetches the online state for every DM partner in
// the joined list. Runs off the UI thread; result returns as presenceOnlineMsg.
func (m *Chat) refreshPartnerPresence() tea.Cmd {
	if m.sess.Presence == nil {
		return nil
	}
	selfHandle := m.sess.Identity.Handle
	partners := make([]string, 0, len(m.joined))
	for _, c := range m.joined {
		if other, ok := dmPartner(c.Name, selfHandle); ok {
			partners = append(partners, other)
		}
	}
	if len(partners) == 0 {
		return nil
	}
	presence := m.sess.Presence
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(2 * time.Second)
		defer cancel()
		online, err := presence.OnlineMany(ctx, partners)
		if err != nil {
			m.sess.Logger.Warn("chat: presence refresh", "err", err)
			return nil
		}
		return presenceOnlineMsg(online)
	}
}
