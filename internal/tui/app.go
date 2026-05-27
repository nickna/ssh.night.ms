// Package tui owns the top-level Bubble Tea program: the root model that
// instantiates the right screen for the current navigation target and
// forwards Update/View through to it.
package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/providers/weather"
	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/screens"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// wallTTL is how long a wall banner stays pinned over the active screen
// before fading. 10s matches the .NET MessageBox prompt — long enough to
// read a 500-char broadcast at a glance, short enough not to occupy the
// header forever.
const wallTTL = 10 * time.Second

// statusBarTempRefresh is how often the status-bar weather widget pulls a
// fresh forecast. The Open-Meteo upstream is cheap but a per-render fetch
// would still be wrong; 30 min matches what the .NET stack's status-bar
// widget did and is plenty given the surface (a single rounded temp).
const statusBarTempRefresh = 30 * time.Minute

// Root is the per-SSH-session tea.Model. Holds the Session and the currently
// active child screen; intercepts global keys (Ctrl+C) and NavigateMsg to
// swap screens. Also owns the wall banner overlay — wall subscription is
// registered in Init and torn down via the cancel closure on Ctrl+C.
type Root struct {
	sess   *session.Session
	screen tea.Model

	// Wall overlay state — pinned text + expiry timestamp. Empty banner
	// suppresses the overlay.
	wallBanner  string
	wallExpires time.Time
	wallCancel  func()

	// Status-bar weather widget. statusTemp is the most recent celsius
	// reading; statusTempOK gates rendering so a fresh session that
	// hasn't yet completed the first forecast fetch shows just the
	// clock instead of "0°" or "—°". Refreshed every statusBarTempRefresh
	// by a tea.Tick.
	statusTemp   float64
	statusTempOK bool
	statusCode   int
}

// NewRoot constructs a Root that lands on the lobby screen. The Session's
// Width/Height come from the SSH PTY allocation and are kept up-to-date as
// tea.WindowSizeMsg arrives.
func NewRoot(sess *session.Session) tea.Model {
	return &Root{
		sess:   sess,
		screen: screens.NewLobby(sess),
	}
}

// wallRecvMsg is what the Subscribe goroutine produces; Update intercepts it
// to update the overlay state and re-arm the subscription.
type wallRecvMsg struct{ msg realtime.WallMessage }

// wallExpireMsg fires from a tea.Tick to clear the banner.
type wallExpireMsg struct{ at time.Time }

// statusTempMsg carries the result of a status-bar weather fetch. ok=false
// silently keeps the previous reading (or "no reading at all" on first
// fetch) — a transient Open-Meteo failure shouldn't blank the widget.
type statusTempMsg struct {
	temp float64
	code int
	ok   bool
}

// statusTempTickMsg fires every statusBarTempRefresh to trigger a refresh.
type statusTempTickMsg struct{}

func (m *Root) Init() tea.Cmd {
	cmds := []tea.Cmd{}
	if m.screen != nil {
		cmds = append(cmds, m.screen.Init())
	}
	if m.sess.Wall != nil {
		ch, cancel := m.sess.Wall.Subscribe()
		m.wallCancel = cancel
		cmds = append(cmds, waitWallCmd(ch))
	}
	if m.sess.Weather != nil {
		cmds = append(cmds, m.fetchStatusTempCmd())
	}
	return tea.Batch(cmds...)
}

// fetchStatusTempCmd kicks off a single weather fetch for the status-bar
// widget. Uses the coords WeatherCoords resolves — saved primary location,
// then legacy profile city. Returns a no-op cmd when the session has no
// known location (status-bar temp stays hidden) so we don't silently show
// a default city's weather. Failure produces ok=false; the Update handler
// keeps any previous reading rather than blanking.
func (m *Root) fetchStatusTempCmd() tea.Cmd {
	provider := m.sess.Weather
	lat, lon, label, ok := m.sess.WeatherCoords()
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f, err := provider.Forecast(ctx, lat, lon, label)
		if err != nil {
			return statusTempMsg{ok: false}
		}
		return statusTempMsg{temp: f.Now.Temperature, code: f.Now.Code, ok: true}
	}
}

// waitWallCmd blocks on the wall channel and returns a tea.Msg when a
// broadcast lands. The cmd re-arms itself in Update so a session sees every
// subsequent broadcast without re-subscribing.
func waitWallCmd(ch <-chan realtime.WallMessage) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		// Stash both the message and the channel so Update can re-arm.
		return wallRecvMsg{msg: msg}
	}
}

func (m *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.sess.Width = msg.Width
		m.sess.Height = msg.Height
		// Fall through so the child can update too if it caches the size.

	case tea.KeyMsg:
		// Global Ctrl+C always quits — every screen would otherwise need to
		// remember to handle it, and forgetting strands the session.
		if msg.String() == "ctrl+c" {
			if m.wallCancel != nil {
				m.wallCancel()
				m.wallCancel = nil
			}
			return m, tea.Quit
		}

	case nav.NavigateMsg:
		return m.route(msg.Target, msg.Arg)

	case wallRecvMsg:
		m.wallBanner = "📢 " + msg.msg.From + ": " + msg.msg.Message
		m.wallExpires = time.Now().Add(wallTTL)
		var cmds []tea.Cmd
		// Re-arm the subscription so the next broadcast also lands.
		if m.sess.Wall != nil {
			// Fresh subscribe per broadcast would leak slots; reuse the
			// session-scoped channel by re-subscribing via the cancel-aware
			// helper below.
			cmds = append(cmds, m.rearmWallCmd())
		}
		cmds = append(cmds, tea.Tick(wallTTL, func(t time.Time) tea.Msg {
			return wallExpireMsg{at: t}
		}))
		return m, tea.Batch(cmds...)

	case wallExpireMsg:
		// Only clear if the expiry matches the latest banner — a newer
		// broadcast inside the TTL would have advanced m.wallExpires.
		if !msg.at.Before(m.wallExpires) {
			m.wallBanner = ""
		}
		return m, nil

	case statusTempMsg:
		if msg.ok {
			m.statusTemp = msg.temp
			m.statusCode = msg.code
			m.statusTempOK = true
		}
		// Schedule the next refresh regardless of success — a transient
		// failure shouldn't permanently stop the widget from updating.
		return m, tea.Tick(statusBarTempRefresh, func(time.Time) tea.Msg {
			return statusTempTickMsg{}
		})

	case statusTempTickMsg:
		if m.sess.Weather == nil {
			return m, nil
		}
		return m, m.fetchStatusTempCmd()
	}

	if m.screen == nil {
		return m, nil
	}
	next, cmd := m.screen.Update(msg)
	m.screen = next
	return m, cmd
}

func (m *Root) View() string {
	if m.screen == nil {
		return ""
	}
	body := m.screen.View()
	width := m.sess.Width
	if width <= 0 {
		width = 80
	}
	height := m.sess.Height
	if height <= 0 {
		height = 24
	}

	// Bottom status bar — always present. One row.
	status := m.renderStatusBar(width)
	// Top wall banner — optional, one row when present.
	hasBanner := m.wallBanner != "" && time.Now().Before(m.wallExpires)
	available := height - 1 // reserve status row
	if hasBanner {
		available-- // reserve banner row
	}
	body = clipLines(body, available)
	if hasBanner {
		banner := wallBannerStyle.Width(width).Render(m.wallBanner)
		return banner + "\n" + body + "\n" + status
	}
	return body + "\n" + status
}

// renderStatusBar is the persistent bottom strip showing the current handle,
// any role badges, and the host on the right. Width-stretched to fill the
// PTY so the dark background draws edge-to-edge.
func (m *Root) renderStatusBar(width int) string {
	handle := m.sess.Identity.Handle
	if handle == "" {
		handle = "anonymous"
	}
	left := " " + handle
	if m.sess.Identity.IsSysop {
		left += "  " + statusSysopStyle.Render("SYSOP")
	}
	right := "night.ms · "
	if m.statusTempOK {
		// Glyph + compact temp (user's preferred unit, no °C/°F suffix —
		// the glyph is the contextual cue). Mirrors the .NET status-bar
		// weather widget.
		right += weather.CodeGlyph(m.statusCode) + " " + m.sess.DisplayPrefs.FormatTemperatureCompact(m.statusTemp) + " · "
	}
	right += m.sess.DisplayPrefs.FormatClock(time.Now()) + " "
	// Compute the gap between left + right so they hug the edges.
	leftLen := lipgloss.Width(left)
	rightLen := lipgloss.Width(right)
	gap := width - leftLen - rightLen
	if gap < 1 {
		gap = 1
	}
	bar := left + strings.Repeat(" ", gap) + right
	return statusBarStyle.Width(width).Render(bar)
}

var (
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color(theme.ColorSurfaceAlt)).
			Foreground(lipgloss.Color(theme.ColorText))
	statusSysopStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(theme.ColorYellow))
)

// clipLines truncates a multi-line string to at most n lines (preserving
// trailing content visibility from the top). When the input is already
// shorter than n, returns it unchanged.
func clipLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	cut := len(s)
	for i, r := range s {
		if r == '\n' {
			count++
			if count == n {
				cut = i
				break
			}
		}
	}
	return s[:cut]
}

var wallBannerStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color(theme.ColorYellow)).
	Background(lipgloss.Color(theme.ColorSurfaceAlt)).
	Padding(0, 1)

// rearmWallCmd re-subscribes to a fresh per-session channel. We need a fresh
// channel each round because the previous waitWallCmd consumed exactly one
// value and returned; reusing the same channel without storing it would lose
// the reference. The dispatcher's Subscribe is cheap (map insert under a
// mutex) and the previous channel's cancel closure handles cleanup.
func (m *Root) rearmWallCmd() tea.Cmd {
	if m.sess.Wall == nil {
		return nil
	}
	// Release the prior slot before grabbing a new one — keeps the
	// dispatcher's subscriber map proportional to live sessions.
	if m.wallCancel != nil {
		m.wallCancel()
	}
	ch, cancel := m.sess.Wall.Subscribe()
	m.wallCancel = cancel
	return waitWallCmd(ch)
}


// route swaps to the screen that owns this destination.
//
// arg carries the optional NavigateMsg payload (e.g. a handle for the Profile
// screen's Finger view). Destinations that don't use a payload ignore it.
//
// Unknown destinations fall back to the lobby — same for a non-sysop that
// somehow reaches DestSysop.
func (m *Root) route(dest nav.Destination, arg string) (tea.Model, tea.Cmd) {
	switch dest {
	case nav.DestLobby:
		m.screen = screens.NewLobby(m.sess)
		return m, m.screen.Init()
	case nav.DestChat:
		m.screen = screens.NewChat(m.sess, m.sess.Chat)
		return m, m.screen.Init()
	case nav.DestBoards:
		m.screen = screens.NewBoards(m.sess)
		return m, m.screen.Init()
	case nav.DestProfile:
		if arg != "" {
			m.screen = screens.NewProfileFinger(m.sess, arg)
		} else {
			m.screen = screens.NewProfile(m.sess)
		}
		return m, m.screen.Init()
	case nav.DestNews:
		m.screen = screens.NewNews(m.sess)
		return m, m.screen.Init()
	case nav.DestWeb:
		m.screen = screens.NewWeb(m.sess)
		return m, m.screen.Init()
	case nav.DestWeather:
		m.screen = screens.NewWeather(m.sess)
		return m, m.screen.Init()
	case nav.DestGallery:
		m.screen = screens.NewGallery(m.sess)
		return m, m.screen.Init()
	case nav.DestFinance:
		m.screen = screens.NewFinance(m.sess)
		return m, m.screen.Init()
	case nav.DestMap:
		m.screen = screens.NewMapScreen(m.sess)
		return m, m.screen.Init()
	case nav.DestDoors:
		m.screen = screens.NewDoors(m.sess)
		return m, m.screen.Init()
	case nav.DestSlots:
		m.screen = screens.NewSlots(m.sess)
		return m, m.screen.Init()
	case nav.DestVideoPoker:
		m.screen = screens.NewVideoPoker(m.sess)
		return m, m.screen.Init()
	case nav.DestBlackjack:
		m.screen = screens.NewBlackjack(m.sess)
		return m, m.screen.Init()
	case nav.DestHoldem:
		m.screen = screens.NewHoldem(m.sess)
		return m, m.screen.Init()
	case nav.DestHoldemMP:
		m.screen = screens.NewHoldemMP(m.sess)
		return m, m.screen.Init()
	case nav.DestLeaderboards:
		m.screen = screens.NewLeaderboards(m.sess)
		return m, m.screen.Init()
	case nav.DestAlerts:
		m.screen = screens.NewAlerts(m.sess)
		return m, m.screen.Init()
	case nav.DestSysop:
		// The lobby only routes sysops here, but defend in depth — a non-
		// sysop hitting this destination via some future code path lands
		// back on the lobby instead of the moderation console.
		if !m.sess.Identity.IsSysop {
			m.screen = screens.NewLobby(m.sess)
			return m, m.screen.Init()
		}
		m.screen = screens.NewSysop(m.sess)
		return m, m.screen.Init()
	case nav.DestLogout:
		return m, tea.Quit
	default:
		m.screen = screens.NewLobby(m.sess)
		return m, m.screen.Init()
	}
}
