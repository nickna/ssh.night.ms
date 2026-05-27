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

// Weather is the Open-Meteo-backed forecast screen. Shows current
// conditions, a 24-hour strip, and a 7-day strip for the session's
// resolved location (Session.WeatherCoords). Sessions with no saved
// location render a "set a location" prompt rather than picking a city
// for the user.
type Weather struct {
	sess     *session.Session
	forecast *weather.Forecast
	loading  bool
	err      string
}

func NewWeather(sess *session.Session) tea.Model { return &Weather{sess: sess, loading: true} }

type weatherLoadedMsg struct {
	forecast *weather.Forecast
	err      error
}

func (m *Weather) Init() tea.Cmd { return m.fetch() }

func (m *Weather) fetch() tea.Cmd {
	if m.sess.Weather == nil {
		return func() tea.Msg { return weatherLoadedMsg{err: fmt.Errorf("weather provider not configured")} }
	}
	provider := m.sess.Weather
	lat, lon, label, ok := m.sess.WeatherCoords()
	if !ok {
		return func() tea.Msg { return weatherLoadedMsg{err: errNoLocation} }
	}
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(12*time.Second)
		defer cancel()
		f, err := provider.Forecast(ctx, lat, lon, label)
		if err != nil {
			return weatherLoadedMsg{err: err}
		}
		return weatherLoadedMsg{forecast: &f}
	}
}

func (m *Weather) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case weatherLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.forecast = msg.forecast
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, nav.Navigate(nav.DestLobby)
		case "r":
			if !m.loading {
				m.loading = true
				m.err = ""
				return m, m.fetch()
			}
		}
	}
	return m, nil
}

var (
	weatherTitle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccent))
	weatherLocation  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorYellow))
	weatherTemp      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorCyan))
	weatherCondition = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorText))
	weatherStat      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorAccentDim))
	weatherHint      = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorMuted)).Italic(true)
	weatherDayHdr    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.ColorAccentDim))
	weatherDayHi     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorYellow))
	weatherDayLo     = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorCyan))
	weatherErrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.ColorRed))
)

func (m *Weather) View() string {
	if m.sess.Width == 0 || m.sess.Height == 0 {
		return "initializing..."
	}
	var b strings.Builder
	b.WriteString(weatherTitle.Render("Weather") + "  " + weatherHint.Render("r refresh · Esc back"))
	b.WriteString("\n\n")

	switch {
	case m.loading:
		b.WriteString(weatherHint.Render("fetching from open-meteo.com…"))
		return b.String()
	case m.err == errNoLocation.Error():
		b.WriteString(weatherHint.Render("Add a location in your profile to see its forecast."))
		return b.String()
	case m.err != "":
		b.WriteString(weatherErrStyle.Render("! " + m.err))
		b.WriteString("\n\n")
		b.WriteString(weatherHint.Render("press r to retry"))
		return b.String()
	case m.forecast == nil:
		b.WriteString(weatherHint.Render("no forecast loaded"))
		return b.String()
	}

	f := m.forecast
	// Header: location + current condition.
	loc := f.Location
	if loc == "" {
		loc = fmt.Sprintf("%.3f, %.3f", f.Latitude, f.Longitude)
	}
	b.WriteString(weatherLocation.Render(loc) + "  " + weatherStat.Render(f.Timezone))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%s  %s %s  %s\n",
		weatherTemp.Render(m.sess.DisplayPrefs.FormatTemperature(f.Now.Temperature)),
		weather.CodeGlyph(f.Now.Code),
		weatherCondition.Render(weather.CodeText(f.Now.Code)),
		weatherStat.Render(fmt.Sprintf("feels like %s · %d%% RH · %.0f km/h",
			m.sess.DisplayPrefs.FormatTemperatureCompact(f.Now.FeelsLike), f.Now.Humidity, f.Now.WindSpeedKmh)),
	))
	b.WriteString("\n")

	// Hourly strip: 12 cells × 6 chars each = 72 chars (fits 80-col).
	hourly := f.Hourly
	if len(hourly) > 12 {
		hourly = hourly[:12]
	}
	if len(hourly) > 0 {
		b.WriteString(weatherDayHdr.Render("next 12 hours"))
		b.WriteString("\n")
		// Row 1: hour labels
		for _, h := range hourly {
			b.WriteString(weatherStat.Render(fmt.Sprintf(" %4s ", m.sess.DisplayPrefs.FormatClock(h.Time))))
		}
		b.WriteString("\n")
		// Row 2: glyphs
		for _, h := range hourly {
			b.WriteString(fmt.Sprintf("  %s   ", weather.CodeGlyph(h.Code)))
		}
		b.WriteString("\n")
		// Row 3: temps
		for _, h := range hourly {
			b.WriteString(weatherTemp.Render(fmt.Sprintf("%5s", m.sess.DisplayPrefs.FormatTemperatureCompact(h.Temperature))) + " ")
		}
		b.WriteString("\n\n")
	}

	// Daily strip: up to 7 days.
	if len(f.Daily) > 0 {
		b.WriteString(weatherDayHdr.Render("7-day forecast"))
		b.WriteString("\n")
		days := f.Daily
		if len(days) > 7 {
			days = days[:7]
		}
		for _, d := range days {
			b.WriteString(fmt.Sprintf("  %s  %s  %s / %s  %s\n",
				weatherStat.Render(d.Date.Format("Mon Jan 2")),
				weather.CodeGlyph(d.Code),
				weatherDayHi.Render(m.sess.DisplayPrefs.FormatTemperatureCompact(d.HighC)),
				weatherDayLo.Render(m.sess.DisplayPrefs.FormatTemperatureCompact(d.LowC)),
				weatherCondition.Render(weather.CodeText(d.Code)),
			))
		}
	}

	b.WriteString("\n")
	b.WriteString(weatherHint.Render(fmt.Sprintf("fetched %s", m.sess.DisplayPrefs.FormatClockWithSeconds(f.FetchedAt))))
	return b.String()
}
