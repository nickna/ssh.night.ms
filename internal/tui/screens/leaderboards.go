package screens

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nickna/ssh.night.ms/internal/realtime"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Leaderboards is a three-view screen over game_rounds, switched with the
// number keys 1/2/3. Each view loads on entry and reloads on tab switch so
// a player who just won big can pop in and see themselves at the top.
type Leaderboards struct {
	sess *session.Session

	view leaderboardView
	rows []realtime.LeaderboardEntry

	loading bool
	err     string
}

type leaderboardView int

const (
	leaderboardViewTopWins leaderboardView = iota
	leaderboardViewLifetimeNet
	leaderboardViewHotStreaks
)

const (
	leaderboardTopN              = 10
	leaderboardHotStreakDays     = 7
	leaderboardLoadTimeoutSecond = 5
)

type leaderboardLoadedMsg struct {
	view leaderboardView
	rows []realtime.LeaderboardEntry
	err  error
}

// NewLeaderboards builds the screen pointed at the "Top Wins" tab. The
// loader fires from Init so the first paint shows "loading…" rather than
// an empty list.
func NewLeaderboards(sess *session.Session) tea.Model {
	return &Leaderboards{sess: sess, view: leaderboardViewTopWins, loading: true}
}

func (m *Leaderboards) Init() tea.Cmd {
	return m.loadCmd(m.view)
}

func (m *Leaderboards) loadCmd(view leaderboardView) tea.Cmd {
	svc := m.sess.Leaderboards
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(leaderboardLoadTimeoutSecond * time.Second)
		defer cancel()
		if svc == nil {
			return leaderboardLoadedMsg{view: view, err: errors.New("leaderboard service unavailable")}
		}
		var (
			rows []realtime.LeaderboardEntry
			err  error
		)
		switch view {
		case leaderboardViewTopWins:
			rows, err = svc.TopSingleWins(ctx, leaderboardTopN)
		case leaderboardViewLifetimeNet:
			rows, err = svc.LifetimeNet(ctx, leaderboardTopN)
		case leaderboardViewHotStreaks:
			rows, err = svc.HotStreaks(ctx, leaderboardTopN, leaderboardHotStreakDays)
		}
		return leaderboardLoadedMsg{view: view, rows: rows, err: err}
	}
}

func (m *Leaderboards) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case leaderboardLoadedMsg:
		// Drop responses for views the user has already tabbed away from —
		// this happens when 1/2/3 land back-to-back faster than the queries.
		if msg.view != m.view {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			m.rows = nil
			return m, nil
		}
		m.err = ""
		m.rows = msg.rows
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, nav.Navigate(nav.DestDoors)
		case "1":
			return m, m.switchTo(leaderboardViewTopWins)
		case "2":
			return m, m.switchTo(leaderboardViewLifetimeNet)
		case "3":
			return m, m.switchTo(leaderboardViewHotStreaks)
		}
	}
	return m, nil
}

// switchTo flips the active view and kicks a fresh load. A no-op when the
// requested view is already active — saves a redundant query if the user
// hammers the same number key.
func (m *Leaderboards) switchTo(view leaderboardView) tea.Cmd {
	if m.view == view {
		return nil
	}
	m.view = view
	m.loading = true
	m.rows = nil
	m.err = ""
	return m.loadCmd(view)
}

func (m *Leaderboards) View() string {
	w, h := m.sess.Width, m.sess.Height
	if w == 0 || h == 0 {
		return "initializing..."
	}

	title := theme.Title.Render("Leaderboards")
	tabs := m.renderTabs()
	header := m.renderViewHeader()
	body := m.renderBody()
	hint := theme.Hint.Render("[1] Top Wins · [2] Lifetime Net · [3] Last 7 Days · Esc back")

	parts := []string{
		title,
		"",
		tabs,
		"",
		header,
		"",
		body,
		"",
		hint,
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Left, parts...))
}

func (m *Leaderboards) renderTabs() string {
	tab := func(v leaderboardView, label string) string {
		if m.view == v {
			return lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(theme.ColorAccent)).
				Background(lipgloss.Color(theme.ColorSurfaceAlt)).
				Padding(0, 1).Render("[ " + label + " ]")
		}
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.ColorAccentDim)).
			Padding(0, 1).Render("  " + label + "  ")
	}
	return strings.Join([]string{
		tab(leaderboardViewTopWins, "Top Wins"),
		tab(leaderboardViewLifetimeNet, "Lifetime Net"),
		tab(leaderboardViewHotStreaks, fmt.Sprintf("Last %d Days", leaderboardHotStreakDays)),
	}, "  ")
}

func (m *Leaderboards) renderViewHeader() string {
	style := lipgloss.NewStyle().Italic(true).
		Foreground(lipgloss.Color(theme.ColorAccentDim))
	switch m.view {
	case leaderboardViewTopWins:
		return style.Render("Biggest single wins ever")
	case leaderboardViewLifetimeNet:
		return style.Render("Cumulative net coins, all games combined")
	case leaderboardViewHotStreaks:
		return style.Render(fmt.Sprintf("Cumulative net coins over the last %d days", leaderboardHotStreakDays))
	}
	return ""
}

func (m *Leaderboards) renderBody() string {
	if m.loading {
		return theme.Hint.Render("loading…")
	}
	if m.err != "" {
		return lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(theme.ColorRed)).Render("! " + m.err)
	}
	if len(m.rows) == 0 {
		return theme.Hint.Render("(no rounds played yet)")
	}
	if m.view == leaderboardViewTopWins {
		return renderTopWinsTable(m.rows)
	}
	return renderCumulativeTable(m.rows)
}

func renderTopWinsTable(rows []realtime.LeaderboardEntry) string {
	var b strings.Builder
	b.WriteString("  #   Handle              Game          Net\n")
	b.WriteString("  ─── ─────────────────── ──────────── ──────\n")
	for _, e := range rows {
		b.WriteString(fmt.Sprintf("  %2d.  %-18s  %-10s  +%5d\n",
			e.Rank, runewidth.Truncate(e.Handle, 18, "…"),
			runewidth.Truncate(e.GameKey, 10, "…"), e.Net))
	}
	return b.String()
}

func renderCumulativeTable(rows []realtime.LeaderboardEntry) string {
	var b strings.Builder
	b.WriteString("  #   Handle              Net\n")
	b.WriteString("  ─── ─────────────────── ───────\n")
	for _, e := range rows {
		b.WriteString(fmt.Sprintf("  %2d.  %-18s  %+7d\n",
			e.Rank, runewidth.Truncate(e.Handle, 18, "…"), e.Net))
	}
	return b.String()
}
