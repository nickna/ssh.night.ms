package session

import (
	"context"
	"fmt"
	"time"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// DisplayPrefs is the per-user formatting slice of a users row. Carries
// the four fields that drive time / date / temperature rendering: IANA
// time zone, clock format (0 = 24-hour, 1 = 12-hour), date format
// (0 = ISO, 1 = US slash, 2 = EU slash), and temperature unit
// (0 = Celsius, 1 = Fahrenheit, 2 = Both). Mirrors UserDisplayExtensions
// in the .NET stack — same pattern (extension-method-style formatters
// keyed on the user) but expressed as receiver methods on this type so
// screens can pass the value around without holding the full users row.
//
// The zero value is safe: UTC + ISO + 24-hour + Celsius. That's what
// brand-new accounts get from the DB defaults too, so a missing cache
// renders the same way a brand-new user would.
type DisplayPrefs struct {
	TimeZoneID      string
	ClockFormat     int32
	DateFormat      int32
	TemperatureUnit int32
}

// Temperature-unit enum values, mirroring the users.temperature_unit
// column. Named constants keep call sites self-documenting; the integer
// values are the DB contract and shouldn't change.
const (
	TempUnitCelsius    int32 = 0
	TempUnitFahrenheit int32 = 1
	TempUnitBoth       int32 = 2
)

// DisplayPrefsFromUser builds DisplayPrefs from a users row.
func DisplayPrefsFromUser(u gen.User) DisplayPrefs {
	return DisplayPrefs{
		TimeZoneID:      u.TimeZoneID,
		ClockFormat:     u.ClockFormat,
		DateFormat:      u.DateFormat,
		TemperatureUnit: u.TemperatureUnit,
	}
}

// resolveLocation maps the stored IANA id to a *time.Location. Stale ids
// (zone retired after the row was written) silently fall back to UTC
// rather than throwing — a broken tz id should never break a render path.
func (d DisplayPrefs) resolveLocation() *time.Location {
	if d.TimeZoneID == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(d.TimeZoneID)
	if err != nil {
		return time.UTC
	}
	return loc
}

// FormatDate renders the date portion in the user's preferred format,
// converted to their time zone first.
func (d DisplayPrefs) FormatDate(t time.Time) string {
	local := t.In(d.resolveLocation())
	switch d.DateFormat {
	case 1:
		return local.Format("1/2/2006")
	case 2:
		return local.Format("2/1/2006")
	default:
		return local.Format("2006-01-02")
	}
}

// FormatClock renders just the wall-clock time (no date) in the user's
// preferred 12/24-hour format.
func (d DisplayPrefs) FormatClock(t time.Time) string {
	local := t.In(d.resolveLocation())
	if d.ClockFormat == 1 {
		return local.Format("3:04 PM")
	}
	return local.Format("15:04")
}

// FormatClockWithSeconds is FormatClock plus seconds — for the freshness
// indicators on Weather + Finance where the user wants to see "data is
// N seconds old," not just "fetched this minute." Matches the .NET
// UserDisplayExtensions.FormatClockWithSeconds helper.
func (d DisplayPrefs) FormatClockWithSeconds(t time.Time) string {
	local := t.In(d.resolveLocation())
	if d.ClockFormat == 1 {
		return local.Format("3:04:05 PM")
	}
	return local.Format("15:04:05")
}

// FormatDateTime renders a combined date + clock value, both in the
// user's preferred format and time zone. The space separator matches the
// .NET FormatDateTime output for cross-stack screen consistency.
func (d DisplayPrefs) FormatDateTime(t time.Time) string {
	return d.FormatDate(t) + " " + d.FormatClock(t)
}

// FormatDayClock renders "<weekday short> <clock>" — used by short-range
// schedules like the NWS alerts screen where the full date would be
// verbose but the day-of-week still matters because the value usually
// crosses a midnight boundary. Weekday name is always English short
// form; localizing weekdays is out of scope.
func (d DisplayPrefs) FormatDayClock(t time.Time) string {
	local := t.In(d.resolveLocation())
	return local.Format("Mon ") + d.FormatClock(t)
}

// FormatTemperature renders a celsius value in the user's preferred
// unit. Used by the prominent "now" + feels-like lines on the Weather
// screen where the unit label is unambiguous. Both mode shows both
// values separated by a slash — matches .NET UserDisplayExtensions.
// FormatTemperature.
func (d DisplayPrefs) FormatTemperature(celsius float64) string {
	switch d.TemperatureUnit {
	case TempUnitFahrenheit:
		return formatTempValue(celsiusToFahrenheit(celsius)) + "°F"
	case TempUnitBoth:
		return formatTempValue(celsius) + "°C/" +
			formatTempValue(celsiusToFahrenheit(celsius)) + "°F"
	default:
		return formatTempValue(celsius) + "°C"
	}
}

// FormatTemperatureCompact renders just the converted value + degree
// sign — no unit letter. Used in space-constrained displays (hourly /
// daily forecast strips) where the prominent "now" line already
// establishes the unit. In Both mode falls back to celsius — both
// would not fit a 5-char cell.
func (d DisplayPrefs) FormatTemperatureCompact(celsius float64) string {
	value := celsius
	if d.TemperatureUnit == TempUnitFahrenheit {
		value = celsiusToFahrenheit(celsius)
	}
	return formatTempValue(value) + "°"
}

func celsiusToFahrenheit(c float64) float64 { return c*9.0/5.0 + 32.0 }

// formatTempValue renders the rounded integer with no leading whitespace.
// Sub-zero values get the negative sign from %.0f naturally.
func formatTempValue(v float64) string {
	// Use %.0f so 22.3 → "22", -3.7 → "-4". Note Go's %.0f rounds half
	// away from zero, matching the .NET F0 specifier behavior.
	return fmt.Sprintf("%.0f", v)
}

// RefreshDisplayPrefs re-reads the user's row and updates the cached
// DisplayPrefs on this Session. Called by the Profile screen after a
// successful save so subsequent renders pick up the new clock / zone /
// date format without re-logging-in. Read failures leave the cache
// untouched — the alternative is wiping perfectly good prefs because of
// a transient DB blip.
func (s *Session) RefreshDisplayPrefs(ctx context.Context) error {
	if s.Queries == nil {
		return nil
	}
	user, err := s.Queries.GetUserByID(ctx, s.Identity.UserID)
	if err != nil {
		return err
	}
	s.DisplayPrefs = DisplayPrefsFromUser(user)
	return nil
}
