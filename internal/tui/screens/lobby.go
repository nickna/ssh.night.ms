package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/providers/weather"
	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Lobby is the carousel hub. Item set, hotkeys, and the conditional
// alerts-key handling mirror src/Night.Ms.SshServer/Tui/Screens/LobbyScreen.cs;
// the visual layout is a Bubble Tea-native take on the .NET LobbyCarouselView.
type Lobby struct {
	sess     *session.Session
	carousel *components.Carousel

	// activeAlerts is the most recent non-empty NWS pull. Rotates through
	// them every alertsRotation seconds via a tea.Tick. Refreshed on entry +
	// every alertsRefresh window.
	activeAlerts []weather.Alert
	alertIndex   int
	alertsLastAt time.Time
}

const (
	alertsRefresh  = 5 * time.Minute
	alertsRotation = 6 * time.Second
)

type lobbyAlertsMsg struct{ alerts []weather.Alert }
type lobbyAlertTickMsg struct{}

// NewLobby builds the lobby with its carousel items. Order and hotkeys match
// .NET LobbyScreen.cs exactly: Chat/Boards/Profile/News/Browser/Gallery/Map/
// Weather/Finance/Doors/[Sysop]/Logout. The Alerts destination is reachable
// via the 'a' shortcut handled by Update, never as a permanent carousel card.
func NewLobby(sess *session.Session) tea.Model {
	icon := func(name string) *art.CellGrid {
		if sess.LobbyIcons == nil {
			return nil
		}
		return sess.LobbyIcons.Get(name)
	}
	items := []components.CarouselItem{
		{Title: "Chat", Hotkey: 'c', Destination: nav.DestChat, Icon: icon("chat")},
		{Title: "Boards", Hotkey: 'b', Destination: nav.DestBoards, Icon: icon("boards")},
		{Title: "Profile", Hotkey: 'p', Destination: nav.DestProfile, Icon: icon("profile")},
		{Title: "News", Hotkey: 'n', Destination: nav.DestNews, Icon: icon("news")},
		{Title: "Browser", Hotkey: 'w', Destination: nav.DestBrowser, Icon: icon("browser")},
		{Title: "Gallery", Hotkey: 'g', Destination: nav.DestGallery, Icon: icon("gallery")},
		{Title: "Map", Hotkey: 'm', Destination: nav.DestMap, Icon: icon("map")},
		{Title: "Weather", Hotkey: 'f', Destination: nav.DestWeather, Icon: icon("weather")},
		{Title: "Finance", Hotkey: 'k', Destination: nav.DestFinance, Icon: icon("finance")},
		{Title: "Doors", Hotkey: 'd', Destination: nav.DestDoors, Icon: icon("doors")},
	}
	if sess.Identity.IsSysop {
		items = append(items, components.CarouselItem{
			Title: "Sysop", Hotkey: 's', Destination: nav.DestSysop, Icon: icon("sysop"),
		})
	}
	items = append(items, components.CarouselItem{
		Title: "Logout", Hotkey: 'l', Destination: nav.DestLogout, Icon: icon("logout"),
	})
	return &Lobby{sess: sess, carousel: components.NewCarousel(items)}
}

func (m *Lobby) Init() tea.Cmd {
	return tea.Batch(m.fetchAlerts(), m.scheduleAlertTick())
}

// fetchAlerts is the alerts-provider read used both on Init and on the
// 5-minute refresh schedule. Failure is silent — no banner is friendlier
// than an error banner on the lobby.
func (m *Lobby) fetchAlerts() tea.Cmd {
	provider := m.sess.Alerts
	lat, lon, _ := m.sess.WeatherCoords()
	return func() tea.Msg {
		if provider == nil {
			return lobbyAlertsMsg{}
		}
		ctx, cancel := m.sess.CtxWithTimeout(8*time.Second)
		defer cancel()
		alerts, err := provider.Alerts(ctx, lat, lon)
		if err != nil {
			return lobbyAlertsMsg{}
		}
		return lobbyAlertsMsg{alerts: alerts}
	}
}

func (m *Lobby) scheduleAlertTick() tea.Cmd {
	return tea.Tick(alertsRotation, func(time.Time) tea.Msg { return lobbyAlertTickMsg{} })
}

func (m *Lobby) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case lobbyAlertsMsg:
		m.activeAlerts = msg.alerts
		m.alertIndex = 0
		m.alertsLastAt = time.Now()
		return m, nil
	case lobbyAlertTickMsg:
		if len(m.activeAlerts) > 1 {
			m.alertIndex = (m.alertIndex + 1) % len(m.activeAlerts)
		}
		var cmds []tea.Cmd
		cmds = append(cmds, m.scheduleAlertTick())
		if time.Since(m.alertsLastAt) >= alertsRefresh {
			cmds = append(cmds, m.fetchAlerts())
		}
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		// Esc on the lobby is logout — same as .NET LobbyScreen.KeyDown. The
		// 'l' hotkey jumps to the Logout card in the carousel; the user still
		// has to press Enter from there. Esc skips that step.
		if msg.String() == "esc" {
			return m, nav.Navigate(nav.DestLogout)
		}
		// 'a' is the alerts shortcut. Active only when we have alerts to show;
		// otherwise it's swallowed so the carousel hotkey path (none matches 'a')
		// doesn't surprise the user.
		if msg.String() == "a" {
			if len(m.activeAlerts) > 0 {
				return m, nav.Navigate(nav.DestAlerts)
			}
			return m, nil
		}
	}
	cmd, dest := m.carousel.Update(msg)
	if dest != nav.DestNone {
		return m, nav.Navigate(dest)
	}
	return m, cmd
}

func (m *Lobby) View() string {
	w, h := m.sess.Width, m.sess.Height
	if w == 0 || h == 0 {
		return "initializing..."
	}

	// Banner — when configured we render the ANSI art; otherwise the title
	// placeholder. Same fall-back chain as the .NET ArtProvider: color grids
	// keep their per-cell styling, plain text picks up the dim Hint scheme
	// .NET applies via BbsTheme.Hint on the fallback Label.
	var banner string
	if m.sess.LoginBanner != nil {
		lb := m.sess.LoginBanner.Banner()
		switch {
		case lb.Grid != nil:
			banner = lb.Render(components.RenderCellGrid)
		case lb.Plain != "":
			banner = theme.Hint.Render(lb.Plain)
		}
	}
	if banner == "" {
		banner = theme.Title.Render("night.ms")
	}

	welcome := theme.Header.Render(fmt.Sprintf("Welcome back, %s.", m.sess.Identity.Handle))

	carousel := m.carousel.View(w)

	hint := theme.Hint.Render(
		"←/→ or h/l to browse · letter to jump · Enter to open · Esc to logout")

	parts := []string{banner, welcome}
	if alert := m.renderAlertStrip(w); alert != "" {
		parts = append(parts, alert)
	}
	parts = append(parts, strings.Repeat("\n", 2))
	carouselIdx := len(parts)
	parts = append(parts, carousel)

	if m.sess.Identity.IsSysop {
		badge := theme.SysopNotice.Render("[ sysop access granted — press S for the console ]")
		parts = append(parts, "", badge)
	}

	parts = append(parts, strings.Repeat("\n", 2), hint)

	body := lipgloss.JoinVertical(lipgloss.Center, parts...)
	m.carousel.SetViewport(0, carouselScreenY(parts, carouselIdx, h, lipgloss.Height(body)))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, body)
}

// carouselScreenY returns the terminal-row coordinate where the carousel's
// top edge will land after lipgloss.Place centers body in a screenH-row
// box. Sums the heights of every part rendered above the carousel inside
// the JoinVertical body, then offsets by the centering padding. Lobby and
// Doors share this helper so their click-targeting math stays in lockstep.
func carouselScreenY(parts []string, carouselIdx, screenH, bodyH int) int {
	top := (screenH - bodyH) / 2
	if top < 0 {
		top = 0
	}
	for i := 0; i < carouselIdx && i < len(parts); i++ {
		top += lipgloss.Height(parts[i])
	}
	return top
}

// renderAlertStrip is the rotating one-line banner showing the currently
// pinned NWS alert. Returns empty when no alerts are active; the lobby skips
// the row entirely in that case so quiet weather doesn't take up vertical
// space. Severity drives the background color: red for extreme/severe,
// muted cyan otherwise.
func (m *Lobby) renderAlertStrip(width int) string {
	if len(m.activeAlerts) == 0 {
		return ""
	}
	a := m.activeAlerts[m.alertIndex%len(m.activeAlerts)]
	label := fmt.Sprintf(" %s · %s ", strings.ToUpper(a.Severity), a.Event)
	if len(m.activeAlerts) > 1 {
		label = fmt.Sprintf(" [%d/%d] %s · press A to open ", m.alertIndex+1, len(m.activeAlerts), strings.TrimSpace(label))
	} else {
		label = fmt.Sprintf("%s· press A to open ", label)
	}
	if width <= 0 {
		width = 80
	}
	style := lipgloss.NewStyle().Background(lipgloss.Color(theme.ColorAccentDim)).Foreground(lipgloss.Color(theme.ColorText))
	switch strings.ToLower(a.Severity) {
	case "extreme", "severe":
		style = lipgloss.NewStyle().Background(lipgloss.Color(theme.ColorRed)).Foreground(lipgloss.Color(theme.ColorText)).Bold(true)
	}
	return style.Width(width).Align(lipgloss.Center).Render(strings.TrimSpace(label))
}

