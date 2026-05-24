package screens

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/tui/art"
	"github.com/nickna/ssh.night.ms/internal/tui/components"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Doors is the games menu. It mirrors the .NET DoorsScreen visually by using
// the shared lobby carousel control — same animation, same per-card icons,
// just a different item set. Each card maps to a nav.Destination; app.go's
// router instantiates the actual game screen.
type Doors struct {
	sess     *session.Session
	carousel *components.Carousel
}

// NewDoors builds the carousel of available games. Hotkeys deliberately
// avoid 'h' and 'l' (which the carousel intercepts as ←/→) so jumps land
// on the right card. Icon names match files under internal/tui/art/lobby-icons.
func NewDoors(sess *session.Session) tea.Model {
	icon := func(name string) *art.CellGrid {
		if sess.LobbyIcons == nil {
			return nil
		}
		return sess.LobbyIcons.Get(name)
	}
	items := []components.CarouselItem{
		{Title: "Slots", Hotkey: 's', Destination: nav.DestSlots, Icon: icon("slots")},
		{Title: "Video Poker", Hotkey: 'v', Destination: nav.DestVideoPoker, Icon: icon("videopoker")},
		{Title: "Blackjack", Hotkey: 'b', Destination: nav.DestBlackjack, Icon: icon("blackjack")},
		{Title: "Hold'em", Hotkey: 't', Destination: nav.DestHoldem, Icon: icon("holdem")},
		{Title: "Hold'em MP", Hotkey: 'm', Destination: nav.DestHoldemMP, Icon: icon("holdem")},
		{Title: "Leaderboards", Hotkey: 'r', Destination: nav.DestLeaderboards, Icon: icon("leaderboards")},
	}
	return &Doors{sess: sess, carousel: components.NewCarousel(items)}
}

func (m *Doors) Init() tea.Cmd { return nil }

func (m *Doors) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if k.String() == "esc" {
			return m, nav.Navigate(nav.DestLobby)
		}
	}
	cmd, dest := m.carousel.Update(msg)
	if dest != nav.DestNone {
		return m, nav.Navigate(dest)
	}
	return m, cmd
}

func (m *Doors) View() string {
	w, h := m.sess.Width, m.sess.Height
	if w == 0 || h == 0 {
		return "initializing..."
	}
	title := theme.Title.Render("Doors")
	welcome := theme.Header.Render("pick your poison")
	carousel := m.carousel.View(w)
	hint := theme.Hint.Render(
		"←/→ or h/l to browse · letter to jump · click or Enter to launch · Esc to lobby")
	parts := []string{title, welcome, strings.Repeat("\n", 2)}
	carouselIdx := len(parts)
	parts = append(parts, carousel, strings.Repeat("\n", 2), hint)
	body := lipgloss.JoinVertical(lipgloss.Center, parts...)
	m.carousel.SetViewport(0, carouselScreenY(parts, carouselIdx, h, lipgloss.Height(body)))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, body)
}
