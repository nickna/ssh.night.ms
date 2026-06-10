package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/weather"
	"github.com/nickna/ssh.night.ms/internal/tui/session"
)

// Weather is the server-rendered web view of the forecast feature. It reuses
// the same Open-Meteo + NWS providers the SSH/TUI path uses (via
// h.deps.Session.Providers) and the same DisplayPrefs formatters, so the two
// surfaces render identical numbers for a given location. The page is
// login-gated and shows the user's primary saved location (Locations.GetPrimary)
// — there is no city picker here; users manage locations under /profile, same
// as the TUI.

const (
	// weatherHourly / weatherDaily cap the strips to match the TUI screen so
	// both surfaces show the same horizon.
	weatherHourly = 12
	weatherDaily  = 7

	// weatherFetchTimeout bounds the upstream Open-Meteo + NWS calls so a slow
	// provider can't hang the request past the 30s router timeout.
	weatherFetchTimeout = 12 * time.Second
)

type weatherNow struct {
	Temp      string
	Glyph     string
	Condition string
	FeelsLike string
	Humidity  int
	Wind      string
}

type weatherHour struct {
	Clock string
	Glyph string
	Temp  string
}

type weatherDay struct {
	Date      string
	Glyph     string
	Hi        string
	Lo        string
	Condition string
}

type weatherAlert struct {
	Severity      string
	SeverityClass string
	Event         string
	Area          string
	Headline      string
	Description   string
	Expires       string
	URL           string
}

type weatherPageData struct {
	pageData
	// Located is false when the user has no saved location; the template then
	// shows a prompt linking to /profile instead of a forecast.
	Located   bool
	Label     string
	Timezone  string
	FetchedAt string
	// FetchError carries an upstream-failure message; the template renders it
	// inline rather than 500-ing the whole page.
	FetchError string
	Now        *weatherNow
	Hourly     []weatherHour
	Daily      []weatherDay
	Alerts     []weatherAlert
}

func (h *handlers) weatherIndex(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Load the user's display prefs so temperatures/clocks render in their
	// chosen unit + zone. A read failure falls back to the zero value (safe:
	// UTC + 24h + Celsius) rather than blocking the page.
	var prefs session.DisplayPrefs
	if user, err := h.deps.Queries.GetUserByID(r.Context(), id.UserID); err == nil {
		prefs = session.DisplayPrefsFromUser(user)
	} else {
		h.deps.Logger.Warn("weather: load prefs", "user_id", id.UserID, "err", err)
	}

	loc, err := h.deps.Locations.GetPrimary(r.Context(), id.UserID)
	if err != nil {
		h.deps.Logger.Error("weather: get primary location", "user_id", id.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if loc == nil {
		// No saved location — render the "add a location" prompt.
		h.renderProfile(w, http.StatusOK, "weather", weatherPageData{
			pageData: h.basePage(r, "weather"),
			Located:  false,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), weatherFetchTimeout)
	defer cancel()

	data := weatherPageData{
		pageData: h.basePage(r, "weather"),
		Located:  true,
		Label:    loc.Label,
	}

	f, err := h.deps.Session.Providers.Weather.Forecast(ctx, loc.Latitude, loc.Longitude, loc.Label)
	if err != nil {
		h.deps.Logger.Error("weather: forecast", "user_id", id.UserID, "err", err)
		data.FetchError = "could not load the forecast — try again in a moment"
		h.renderProfile(w, http.StatusOK, "weather", data)
		return
	}

	data.Timezone = f.Timezone
	data.FetchedAt = prefs.FormatClockWithSeconds(f.FetchedAt)
	data.Now = &weatherNow{
		Temp:      prefs.FormatTemperature(f.Now.Temperature),
		Glyph:     weather.CodeGlyph(f.Now.Code),
		Condition: weather.CodeText(f.Now.Code),
		FeelsLike: prefs.FormatTemperatureCompact(f.Now.FeelsLike),
		Humidity:  f.Now.Humidity,
		Wind:      fmt.Sprintf("%.0f km/h", f.Now.WindSpeedKmh),
	}

	hourly := f.Hourly
	if len(hourly) > weatherHourly {
		hourly = hourly[:weatherHourly]
	}
	for _, hr := range hourly {
		data.Hourly = append(data.Hourly, weatherHour{
			Clock: prefs.FormatClockLocal(hr.Time),
			Glyph: weather.CodeGlyph(hr.Code),
			Temp:  prefs.FormatTemperatureCompact(hr.Temperature),
		})
	}

	days := f.Daily
	if len(days) > weatherDaily {
		days = days[:weatherDaily]
	}
	for _, d := range days {
		data.Daily = append(data.Daily, weatherDay{
			Date:      d.Date.Format("Mon Jan 2"),
			Glyph:     weather.CodeGlyph(d.Code),
			Hi:        prefs.FormatTemperatureCompact(d.HighC),
			Lo:        prefs.FormatTemperatureCompact(d.LowC),
			Condition: weather.CodeText(d.Code),
		})
	}

	// Active NWS alerts — best-effort. A nil provider or a fetch error just
	// drops the alerts section; it never blanks the forecast above. NWS only
	// covers the US, so non-US locations come back empty by design.
	if h.deps.Session.Providers.Alerts != nil {
		if alerts, err := h.deps.Session.Providers.Alerts.Alerts(ctx, loc.Latitude, loc.Longitude); err != nil {
			h.deps.Logger.Warn("weather: alerts", "user_id", id.UserID, "err", err)
		} else {
			for _, a := range alerts {
				data.Alerts = append(data.Alerts, weatherAlert{
					Severity:      a.Severity,
					SeverityClass: severityClass(a.Severity),
					Event:         a.Event,
					Area:          a.Area,
					Headline:      a.Headline,
					Description:   a.Description,
					Expires:       prefs.FormatDayClock(a.Expires),
					URL:           a.URL,
				})
			}
		}
	}

	h.renderProfile(w, http.StatusOK, "weather", data)
}

// severityClass maps an NWS severity to a CSS modifier class. Mirrors the
// TUI's severityBadge color mapping (alerts.go) so the two surfaces agree on
// what "severe" looks like. Unknown / empty severities fall back to the
// dim "minor" treatment.
func severityClass(severity string) string {
	switch strings.ToLower(severity) {
	case "extreme":
		return "sev-extreme"
	case "severe":
		return "sev-severe"
	case "moderate":
		return "sev-moderate"
	default:
		return "sev-minor"
	}
}
