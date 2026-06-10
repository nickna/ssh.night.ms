// Package weather defines the weather provider contract + an Open-Meteo
// implementation. Open-Meteo is keyless and free, which keeps the BBS easy
// to self-host. NWS-style alerts can layer on later via a second provider.
package weather

import (
	"context"
	"time"
)

// Conditions is the current-weather snapshot.
type Conditions struct {
	Temperature   float64 // Celsius (rendering layer converts as needed)
	FeelsLike     float64
	Humidity      int
	WindSpeedKmh  float64
	WindDirection int // degrees
	Code          int // WMO weather code (use CodeText for label)
}

// HourSlot is one hour of an hourly forecast.
type HourSlot struct {
	Time        time.Time
	Temperature float64
	Code        int
}

// DaySlot is one day of a daily forecast.
type DaySlot struct {
	Date    time.Time
	HighC   float64
	LowC    float64
	Code    int
	Sunrise time.Time
	Sunset  time.Time
}

// Forecast bundles current + hourly + daily into one Provider return.
type Forecast struct {
	Location  string // human label, e.g. "New York"
	Latitude  float64
	Longitude float64
	Timezone  string
	Now       Conditions
	Hourly    []HourSlot // typically next 24 entries
	Daily     []DaySlot  // typically next 7 entries
	FetchedAt time.Time
}

// Provider is the contract every weather backend implements.
type Provider interface {
	Forecast(ctx context.Context, lat, lon float64, label string) (Forecast, error)
}

// CodeText returns a short human label for a WMO weather code (subset; full
// table at https://open-meteo.com/en/docs). Falls back to "code N" so the
// UI never goes blank on an unrecognized value.
func CodeText(code int) string {
	switch code {
	case 0:
		return "clear"
	case 1:
		return "mainly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75:
		return "snow"
	case 77:
		return "snow grains"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorm"
	case 96, 99:
		return "thunderstorm + hail"
	}
	return "—"
}

// CodeGlyph returns a single character that depicts the weather code at a
// glance. Helps the hourly strip stay compact.
func CodeGlyph(code int) string {
	switch code {
	case 0, 1:
		return "☀"
	case 2:
		return "⛅"
	case 3:
		return "☁"
	case 45, 48:
		return "≡"
	case 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82:
		return "☂"
	case 71, 73, 75, 77, 85, 86:
		return "❄"
	case 95, 96, 99:
		return "⚡"
	}
	return "·"
}
