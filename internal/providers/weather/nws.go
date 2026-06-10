package weather

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/httpjson"
)

// Alert is one active National Weather Service alert at a lat/lon. Rendered
// on the lobby's Alerts destination + as a header strip during severe events,
// with a future opportunity to broadcast over the wall pipe.
type Alert struct {
	ID          string // NWS feature id; stable for the lifetime of the alert
	Event       string // "Severe Thunderstorm Warning", "Tornado Watch", etc.
	Severity    string // "Extreme" / "Severe" / "Moderate" / "Minor" / "Unknown"
	Headline    string // one-line summary
	Description string // full text, multi-line
	Area        string // free-form area description
	Sender      string // issuing office
	Effective   time.Time
	Expires     time.Time
	URL         string
}

// AlertProvider is the contract for fetching active alerts at a coordinate.
// The implementation must be safe to call concurrently from many sessions.
type AlertProvider interface {
	Alerts(ctx context.Context, lat, lon float64) ([]Alert, error)
}

// NWSAlerts implements AlertProvider against api.weather.gov. NWS only
// covers the US; outside the US the endpoint returns an empty feature
// collection (handled gracefully — no error, just no alerts).
//
// The API requires a contact User-Agent. UserAgent defaults to an
// identifier; deployers can override via NewNWSAlerts.
type NWSAlerts struct {
	HTTP      *http.Client
	UserAgent string
}

// NewNWSAlerts builds an NWS alert provider with a 10-second HTTP timeout
// and a default User-Agent. Pass "" to keep the default.
func NewNWSAlerts(userAgent string) *NWSAlerts {
	if userAgent == "" {
		userAgent = "ssh.night.ms-go (https://github.com/nickna/ssh.night.ms)"
	}
	return &NWSAlerts{
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		UserAgent: userAgent,
	}
}

// nwsResponse is the slice of the GeoJSON we actually care about. NWS's
// schema has many more fields; we ignore everything we don't render.
type nwsResponse struct {
	Features []struct {
		Properties struct {
			ID          string    `json:"id"`
			Event       string    `json:"event"`
			Severity    string    `json:"severity"`
			Headline    string    `json:"headline"`
			Description string    `json:"description"`
			AreaDesc    string    `json:"areaDesc"`
			SenderName  string    `json:"senderName"`
			Effective   time.Time `json:"effective"`
			Expires     time.Time `json:"expires"`
			Web         string    `json:"web"`
		} `json:"properties"`
	} `json:"features"`
}

// Alerts queries https://api.weather.gov/alerts/active?point=lat,lon and
// converts the response into our Alert struct. Returns an empty (non-nil
// when there are no alerts) slice; the caller should treat nil as "fetch
// failed" via the error path, not "no alerts".
func (n *NWSAlerts) Alerts(ctx context.Context, lat, lon float64) ([]Alert, error) {
	url := fmt.Sprintf("https://api.weather.gov/alerts/active?point=%.4f,%.4f", lat, lon)
	headers := map[string]string{
		"Accept":     "application/geo+json",
		"User-Agent": n.UserAgent,
	}
	var payload nwsResponse
	if err := httpjson.Get(ctx, n.HTTP, url, &payload, headers); err != nil {
		return nil, fmt.Errorf("nws: %w", err)
	}

	out := make([]Alert, 0, len(payload.Features))
	for _, f := range payload.Features {
		out = append(out, Alert{
			ID:          f.Properties.ID,
			Event:       f.Properties.Event,
			Severity:    f.Properties.Severity,
			Headline:    f.Properties.Headline,
			Description: f.Properties.Description,
			Area:        f.Properties.AreaDesc,
			Sender:      f.Properties.SenderName,
			Effective:   f.Properties.Effective,
			Expires:     f.Properties.Expires,
			URL:         f.Properties.Web,
		})
	}
	// Highest severity first, then earliest expiry — gives the user
	// the most urgent alert at the top regardless of issue order.
	sort.SliceStable(out, func(i, j int) bool {
		si := severityRank(out[i].Severity)
		sj := severityRank(out[j].Severity)
		if si != sj {
			return si > sj
		}
		return out[i].Expires.Before(out[j].Expires)
	})
	return out, nil
}

// severityRank converts the NWS-defined severity strings to a sortable int.
// Higher rank = more severe. Unknown maps to 0 so it sinks to the bottom.
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "extreme":
		return 4
	case "severe":
		return 3
	case "moderate":
		return 2
	case "minor":
		return 1
	}
	return 0
}
