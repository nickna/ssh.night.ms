package weather

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/httpjson"
)

// OpenMeteo fetches forecasts from https://api.open-meteo.com (free, no key).
// One HTTP request per Forecast() call returns current + hourly + daily in
// the same payload, which keeps the screen fast on first paint.
type OpenMeteo struct {
	HTTPClient *http.Client
	// HourlyHours limits how many hourly entries to keep. The API gives 168
	// (7 days × 24) by default; we'd rather not stream that to every BBS
	// session, so cap at 24 unless overridden.
	HourlyHours int
}

func NewOpenMeteo() *OpenMeteo {
	return &OpenMeteo{
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
		HourlyHours: 24,
	}
}

const openMeteoBase = "https://api.open-meteo.com/v1/forecast"

// openMeteoResponse mirrors the (loose) JSON shape Open-Meteo returns. We
// accept far more fields than we use — the unknown ones decode into nothing
// since we use struct tags only for what we read.
type openMeteoResponse struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	Current   struct {
		Time         string  `json:"time"`
		Temperature  float64 `json:"temperature_2m"`
		ApparentTemp float64 `json:"apparent_temperature"`
		Humidity     int     `json:"relative_humidity_2m"`
		WeatherCode  int     `json:"weather_code"`
		WindSpeed    float64 `json:"wind_speed_10m"`
		WindDir      int     `json:"wind_direction_10m"`
	} `json:"current"`
	Hourly struct {
		Time         []string  `json:"time"`
		Temperature  []float64 `json:"temperature_2m"`
		WeatherCode  []int     `json:"weather_code"`
	} `json:"hourly"`
	Daily struct {
		Time          []string  `json:"time"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		WeatherCode   []int     `json:"weather_code"`
		Sunrise       []string  `json:"sunrise"`
		Sunset        []string  `json:"sunset"`
	} `json:"daily"`
}

func (p *OpenMeteo) Forecast(ctx context.Context, lat, lon float64, label string) (Forecast, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', 4, 64))
	q.Set("current", "temperature_2m,apparent_temperature,relative_humidity_2m,weather_code,wind_speed_10m,wind_direction_10m")
	q.Set("hourly", "temperature_2m,weather_code")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,weather_code,sunrise,sunset")
	q.Set("timezone", "auto")
	q.Set("forecast_days", "7")

	var raw openMeteoResponse
	if err := httpjson.Get(ctx, p.HTTPClient, openMeteoBase+"?"+q.Encode(), &raw, nil); err != nil {
		return Forecast{}, fmt.Errorf("open-meteo: %w", err)
	}

	loc, _ := time.LoadLocation(raw.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	parse := func(s string) time.Time {
		// Open-Meteo emits naive timestamps like "2026-05-22T14:00" without
		// offset; they're already in the requested timezone.
		t, _ := time.ParseInLocation("2006-01-02T15:04", s, loc)
		return t
	}

	out := Forecast{
		Location:  label,
		Latitude:  raw.Latitude,
		Longitude: raw.Longitude,
		Timezone:  raw.Timezone,
		Now: Conditions{
			Temperature:   raw.Current.Temperature,
			FeelsLike:     raw.Current.ApparentTemp,
			Humidity:      raw.Current.Humidity,
			WindSpeedKmh:  raw.Current.WindSpeed,
			WindDirection: raw.Current.WindDir,
			Code:          raw.Current.WeatherCode,
		},
		FetchedAt: time.Now().UTC(),
	}

	// Hourly: trim to HourlyHours starting at the FIRST hour ≥ now.
	limit := p.HourlyHours
	if limit <= 0 {
		limit = 24
	}
	now := time.Now()
	startIdx := 0
	for i, ts := range raw.Hourly.Time {
		if parse(ts).Before(now) {
			startIdx = i + 1
		} else {
			break
		}
	}
	for i := startIdx; i < len(raw.Hourly.Time) && len(out.Hourly) < limit; i++ {
		out.Hourly = append(out.Hourly, HourSlot{
			Time:        parse(raw.Hourly.Time[i]),
			Temperature: raw.Hourly.Temperature[i],
			Code:        raw.Hourly.WeatherCode[i],
		})
	}

	for i := range raw.Daily.Time {
		out.Daily = append(out.Daily, DaySlot{
			Date:    parse(raw.Daily.Time[i] + "T00:00"),
			HighC:   raw.Daily.TempMax[i],
			LowC:    raw.Daily.TempMin[i],
			Code:    raw.Daily.WeatherCode[i],
			Sunrise: parse(raw.Daily.Sunrise[i]),
			Sunset:  parse(raw.Daily.Sunset[i]),
		})
	}
	return out, nil
}
