package session

import (
	"testing"
	"time"
)

// 2026-04-15 13:07 UTC — a fixed instant we walk every format permutation
// against. Lands well inside DST for the US/EU test zones so the offset
// math is exercised on a non-trivial day.
var refTime = time.Date(2026, 4, 15, 13, 7, 0, 0, time.UTC)

func TestDisplayPrefsFormatDate(t *testing.T) {
	cases := []struct {
		name string
		p    DisplayPrefs
		want string
	}{
		{"iso default", DisplayPrefs{}, "2026-04-15"},
		{"iso explicit", DisplayPrefs{DateFormat: 0}, "2026-04-15"},
		{"us slash", DisplayPrefs{DateFormat: 1}, "4/15/2026"},
		{"eu slash", DisplayPrefs{DateFormat: 2}, "15/4/2026"},
		{"tz shifts day", DisplayPrefs{TimeZoneID: "Asia/Tokyo"}, "2026-04-15"}, // still same day at 22:07 JST
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.FormatDate(refTime)
			if got != tc.want {
				t.Errorf("FormatDate(%+v) = %q, want %q", tc.p, got, tc.want)
			}
		})
	}
}

func TestDisplayPrefsFormatClock(t *testing.T) {
	cases := []struct {
		name string
		p    DisplayPrefs
		want string
	}{
		{"24h utc default", DisplayPrefs{}, "13:07"},
		{"24h explicit", DisplayPrefs{ClockFormat: 0}, "13:07"},
		{"12h", DisplayPrefs{ClockFormat: 1}, "1:07 PM"},
		{"24h NY", DisplayPrefs{TimeZoneID: "America/New_York"}, "09:07"},
		{"12h NY", DisplayPrefs{TimeZoneID: "America/New_York", ClockFormat: 1}, "9:07 AM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.FormatClock(refTime)
			if got != tc.want {
				t.Errorf("FormatClock(%+v) = %q, want %q", tc.p, got, tc.want)
			}
		})
	}
}

// Stale / malformed tz ids must not break rendering; they fall back to UTC
// silently. This guard exists because TimeZoneInfo entries can be retired
// between when a row was written and when it's read.
func TestDisplayPrefsBadTimezoneFallsBackToUTC(t *testing.T) {
	p := DisplayPrefs{TimeZoneID: "Mars/Olympus_Mons", ClockFormat: 0}
	if got, want := p.FormatClock(refTime), "13:07"; got != want {
		t.Errorf("bad tz fallback: got %q, want %q (UTC)", got, want)
	}
}

func TestDisplayPrefsFormatDateTime(t *testing.T) {
	p := DisplayPrefs{TimeZoneID: "America/New_York", ClockFormat: 1, DateFormat: 1}
	got := p.FormatDateTime(refTime)
	want := "4/15/2026 9:07 AM"
	if got != want {
		t.Errorf("FormatDateTime = %q, want %q", got, want)
	}
}

func TestDisplayPrefsFormatClockWithSeconds(t *testing.T) {
	cases := []struct {
		name string
		p    DisplayPrefs
		want string
	}{
		{"24h utc", DisplayPrefs{}, "13:07:00"},
		{"12h utc", DisplayPrefs{ClockFormat: 1}, "1:07:00 PM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FormatClockWithSeconds(refTime); got != tc.want {
				t.Errorf("FormatClockWithSeconds = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisplayPrefsFormatTemperature(t *testing.T) {
	cases := []struct {
		name    string
		p       DisplayPrefs
		celsius float64
		want    string
	}{
		{"celsius default", DisplayPrefs{}, 22, "22°C"},
		{"celsius explicit", DisplayPrefs{TemperatureUnit: TempUnitCelsius}, 22, "22°C"},
		{"fahrenheit", DisplayPrefs{TemperatureUnit: TempUnitFahrenheit}, 22, "72°F"},
		{"both", DisplayPrefs{TemperatureUnit: TempUnitBoth}, 22, "22°C/72°F"},
		{"freezing celsius", DisplayPrefs{}, 0, "0°C"},
		{"freezing fahrenheit", DisplayPrefs{TemperatureUnit: TempUnitFahrenheit}, 0, "32°F"},
		{"negative celsius", DisplayPrefs{}, -10, "-10°C"},
		{"negative fahrenheit", DisplayPrefs{TemperatureUnit: TempUnitFahrenheit}, -10, "14°F"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FormatTemperature(tc.celsius); got != tc.want {
				t.Errorf("FormatTemperature(%v) = %q, want %q", tc.celsius, got, tc.want)
			}
		})
	}
}

func TestDisplayPrefsFormatTemperatureCompact(t *testing.T) {
	cases := []struct {
		name    string
		p       DisplayPrefs
		celsius float64
		want    string
	}{
		{"celsius", DisplayPrefs{}, 22, "22°"},
		{"fahrenheit", DisplayPrefs{TemperatureUnit: TempUnitFahrenheit}, 22, "72°"},
		{"both falls back to celsius", DisplayPrefs{TemperatureUnit: TempUnitBoth}, 22, "22°"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FormatTemperatureCompact(tc.celsius); got != tc.want {
				t.Errorf("FormatTemperatureCompact(%v) = %q, want %q", tc.celsius, got, tc.want)
			}
		})
	}
}

// FormatClockLocal must render the timestamp's OWN zone and honor only the
// 12/24-hour preference — it must NOT re-zone to the user's TimeZoneID. This
// is what keeps a weather forecast on the forecast city's clock regardless of
// where the viewer is.
func TestDisplayPrefsFormatClockLocal(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load Asia/Tokyo: %v", err)
	}
	// 22:07 Tokyo == refTime (13:07 UTC).
	tokyoTime := refTime.In(tokyo)
	cases := []struct {
		name string
		p    DisplayPrefs
		want string
	}{
		// User's zone is New York, but the value stays on Tokyo's wall clock.
		{"24h ignores user zone", DisplayPrefs{TimeZoneID: "America/New_York"}, "22:07"},
		{"12h ignores user zone", DisplayPrefs{TimeZoneID: "America/New_York", ClockFormat: 1}, "10:07 PM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FormatClockLocal(tokyoTime); got != tc.want {
				t.Errorf("FormatClockLocal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisplayPrefsFormatStamp(t *testing.T) {
	// 2026-04-15 13:07 UTC.
	cases := []struct {
		name string
		p    DisplayPrefs
		want string
	}{
		{"24h utc", DisplayPrefs{}, "Apr 15, 13:07"},
		{"12h utc", DisplayPrefs{ClockFormat: 1}, "Apr 15, 1:07 PM"},
		{"24h NY", DisplayPrefs{TimeZoneID: "America/New_York"}, "Apr 15, 09:07"},
		{"far-west tz still same day", DisplayPrefs{TimeZoneID: "Pacific/Honolulu"}, "Apr 15, 03:07"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FormatStamp(refTime); got != tc.want {
				t.Errorf("FormatStamp = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisplayPrefsFormatDayClock(t *testing.T) {
	// 2026-04-15 13:07 UTC is a Wednesday.
	cases := []struct {
		name string
		p    DisplayPrefs
		want string
	}{
		{"24h utc", DisplayPrefs{}, "Wed 13:07"},
		{"12h utc", DisplayPrefs{ClockFormat: 1}, "Wed 1:07 PM"},
		{"24h NY (still Wed)", DisplayPrefs{TimeZoneID: "America/New_York"}, "Wed 09:07"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.FormatDayClock(refTime); got != tc.want {
				t.Errorf("FormatDayClock = %q, want %q", got, tc.want)
			}
		})
	}
}
