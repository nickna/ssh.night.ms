package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/providers/weather"
	"github.com/nickna/ssh.night.ms/internal/tui/nav"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
	"github.com/nickna/ssh.night.ms/internal/tui/theme"
)

// Alerts renders the active NWS alerts for the session's configured weather
// coordinate. The user picks one with ↑/↓ + Enter to read the full body;
// Esc returns to the lobby. Outside the US, NWS returns zero features and
// we show a friendly "no alerts" empty state.
type Alerts struct {
	sess    *session.Session
	loading bool
	err     string

	alerts []weather.Alert
	cursor int

	mode      alertsMode
	detail    *weather.Alert
	detailWrap int
}

type alertsMode int

const (
	alertsList alertsMode = iota
	alertsDetail
)

func NewAlerts(sess *session.Session) tea.Model {
	return &Alerts{sess: sess, loading: true}
}

type alertsLoadedMsg struct {
	alerts []weather.Alert
	err    error
}

func (m *Alerts) Init() tea.Cmd { return m.loadCmd() }

func (m *Alerts) loadCmd() tea.Cmd {
	provider := m.sess.Alerts
	lat, lon, _ := m.sess.WeatherCoords()
	return func() tea.Msg {
		if provider == nil {
			return alertsLoadedMsg{err: fmt.Errorf("alerts provider not configured")}
		}
		ctx, cancel := m.sess.CtxWithTimeout(12*time.Second)
		defer cancel()
		alerts, err := provider.Alerts(ctx, lat, lon)
		return alertsLoadedMsg{alerts: alerts, err: err}
	}
}

func (m *Alerts) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case alertsLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.alerts = msg.alerts
		if m.cursor >= len(m.alerts) {
			m.cursor = 0
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.mode == alertsDetail {
				m.mode = alertsList
				m.detail = nil
				return m, nil
			}
			return m, nav.Navigate(nav.DestLobby)
		case "r":
			m.loading = true
			return m, m.loadCmd()
		case "up", "k":
			if m.mode == alertsList && m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.mode == alertsList && m.cursor < len(m.alerts)-1 {
				m.cursor++
			}
		case "enter":
			if m.mode == alertsList && m.cursor < len(m.alerts) {
				m.detail = &m.alerts[m.cursor]
				m.mode = alertsDetail
				m.detailWrap = 0
			}
		case "pgup":
			if m.mode == alertsDetail {
				m.detailWrap -= 10
				if m.detailWrap < 0 {
					m.detailWrap = 0
				}
			}
		case "pgdown":
			if m.mode == alertsDetail {
				m.detailWrap += 10
			}
		}
	}
	return m, nil
}

var (
	alertsTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	alertsHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	alertsExtreme  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorRed))
	alertsSevere   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	alertsModerate = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	alertsMinor    = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorDim))
	alertsBody     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	alertsArea     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim)).Italic(true)
	alertsErr      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

func (m *Alerts) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	_, _, label := m.sess.WeatherCoords()

	var b strings.Builder
	b.WriteString(alertsTitle.Render("Active alerts — " + label))
	b.WriteString("  ")
	if m.mode == alertsDetail {
		b.WriteString(alertsHint.Render("Esc: back · PgUp/PgDn: scroll · r: refresh"))
	} else {
		b.WriteString(alertsHint.Render("↑/↓: select · Enter: open · r: refresh · Esc: lobby"))
	}
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString(alertsHint.Render("loading…"))
		return b.String()
	}
	if m.err != "" {
		b.WriteString(alertsErr.Render("! " + m.err))
		return b.String()
	}

	if m.mode == alertsDetail && m.detail != nil {
		b.WriteString(m.renderDetail(*m.detail))
		return b.String()
	}

	if len(m.alerts) == 0 {
		b.WriteString(alertsHint.Render("no active alerts for this location."))
		b.WriteString("\n\n")
		b.WriteString(alertsHint.Render("NWS only covers the US — locations outside come back empty by design."))
		return b.String()
	}

	for i, a := range m.alerts {
		prefix := "  "
		if i == m.cursor {
			prefix = "▸ "
		}
		row := fmt.Sprintf("%s%s  %s", prefix, severityBadge(a.Severity), a.Event)
		b.WriteString(row)
		b.WriteString("\n")
		b.WriteString("    " + alertsArea.Render(truncateArea(a.Area, m.sess.Width-6)))
		b.WriteString("\n")
		b.WriteString("    " + alertsHint.Render("expires "+m.sess.DisplayPrefs.FormatDayClock(a.Expires)))
		b.WriteString("\n\n")
	}
	return b.String()
}

func (m *Alerts) renderDetail(a weather.Alert) string {
	width := m.sess.Width - 4
	if width < 30 {
		width = 30
	}
	var b strings.Builder
	b.WriteString(severityBadge(a.Severity) + "  " + alertsTitle.Render(a.Event))
	b.WriteString("\n")
	if a.Area != "" {
		for _, line := range wrapToWidth(a.Area, width) {
			b.WriteString(alertsArea.Render(line) + "\n")
		}
	}
	b.WriteString("\n")
	if a.Headline != "" {
		for _, line := range wrapToWidth(a.Headline, width) {
			b.WriteString(alertsBody.Render(line) + "\n")
		}
		b.WriteString("\n")
	}

	for _, paragraph := range strings.Split(a.Description, "\n\n") {
		clean := strings.TrimSpace(strings.ReplaceAll(paragraph, "\n", " "))
		if clean == "" {
			continue
		}
		for _, line := range wrapToWidth(clean, width) {
			b.WriteString(alertsBody.Render(line) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(alertsHint.Render(fmt.Sprintf(
		"issued by %s · effective %s · expires %s",
		a.Sender,
		m.sess.DisplayPrefs.FormatDayClock(a.Effective),
		m.sess.DisplayPrefs.FormatDayClock(a.Expires),
	)))
	if a.URL != "" {
		b.WriteString("\n" + alertsHint.Render(a.URL))
	}

	// Apply scroll. Done at the end so wrapping and metadata stay together.
	all := strings.Split(b.String(), "\n")
	if m.detailWrap > 0 && m.detailWrap < len(all) {
		all = all[m.detailWrap:]
	}
	availH := m.sess.Height - 4
	if availH > 0 && len(all) > availH {
		all = all[:availH]
	}
	return strings.Join(all, "\n")
}

func severityBadge(severity string) string {
	label := strings.ToUpper(severity)
	if label == "" {
		label = "UNKNOWN"
	}
	switch strings.ToLower(severity) {
	case "extreme":
		return alertsExtreme.Render("[" + label + "]")
	case "severe":
		return alertsSevere.Render("[" + label + "]")
	case "moderate":
		return alertsModerate.Render("[" + label + "]")
	case "minor":
		return alertsMinor.Render("[" + label + "]")
	}
	return alertsMinor.Render("[" + label + "]")
}

func truncateArea(s string, max int) string {
	if max <= 1 {
		return s
	}
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
