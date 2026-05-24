package finance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Frankfurter delivers FX rates via api.frankfurter.dev (ECB reference rates,
// ~30 currencies, daily granularity, no key). Canonical is the 6-char
// concatenated form "EURUSD" produced by Resolve. Since the data is daily,
// the sparkline is 30 EOD points and the detail series is 365 EOD points;
// "change" is computed from yesterday vs today rather than provided directly.
type Frankfurter struct {
	HTTPClient *http.Client
}

func NewFrankfurter() *Frankfurter {
	return &Frankfurter{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

func splitFxPair(canonical string) (base, quote string, ok bool) {
	s := canonical
	if len(s) == 7 && s[3] == '/' {
		return s[:3], s[4:], true
	}
	if len(s) == 6 {
		return s[:3], s[3:], true
	}
	return "", "", false
}

// fetchSeries calls /v1/{from}..{to}?base={base}&symbols={quote} and returns
// the rates in chronological order so the last element is the most recent.
func (f *Frankfurter) fetchSeries(ctx context.Context, base, quote string, days int) ([]float64, error) {
	to := time.Now().UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -days)
	url := fmt.Sprintf(
		"https://api.frankfurter.dev/v1/%s..%s?base=%s&symbols=%s",
		from.Format("2006-01-02"), to.Format("2006-01-02"), base, quote,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nightms-bbs/1.0 (+https://night.ms)")
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frankfurter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("frankfurter: status %d", resp.StatusCode)
	}
	var body struct {
		Rates map[string]map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("frankfurter: decode: %w", err)
	}
	type dp struct {
		Date string
		Rate float64
	}
	pts := make([]dp, 0, len(body.Rates))
	for date, m := range body.Rates {
		if r, ok := m[quote]; ok {
			pts = append(pts, dp{date, r})
		}
	}
	if len(pts) == 0 {
		return nil, fmt.Errorf("frankfurter: no rates for %s/%s", base, quote)
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Date < pts[j].Date })
	out := make([]float64, len(pts))
	for i, p := range pts {
		out[i] = p.Rate
	}
	return out, nil
}

func (f *Frankfurter) GetQuote(ctx context.Context, canonical string) (*Quote, error) {
	base, quote, ok := splitFxPair(canonical)
	if !ok {
		return nil, fmt.Errorf("frankfurter: bad pair %q", canonical)
	}
	series, err := f.fetchSeries(ctx, base, quote, 30)
	if err != nil {
		return nil, err
	}
	price := series[len(series)-1]
	prev := price
	if len(series) >= 2 {
		prev = series[len(series)-2]
	}
	change := price - prev
	var changePct float64
	if prev != 0 {
		changePct = (change / prev) * 100
	}
	return &Quote{
		Symbol:       canonical,
		Display:      base + "/" + quote,
		Name:         base + "/" + quote,
		Currency:     quote,
		PriceUSD:     price,
		Change24hUSD: change,
		Change24hPct: changePct,
		Last:         time.Now(),
	}, nil
}

func (f *Frankfurter) GetSparkline(ctx context.Context, canonical string) ([]float64, error) {
	base, quote, ok := splitFxPair(canonical)
	if !ok {
		return nil, fmt.Errorf("frankfurter: bad pair %q", canonical)
	}
	return f.fetchSeries(ctx, base, quote, 30)
}

func (f *Frankfurter) GetDetail(ctx context.Context, canonical string) (*Detail, error) {
	base, quote, ok := splitFxPair(canonical)
	if !ok {
		return nil, fmt.Errorf("frankfurter: bad pair %q", canonical)
	}
	q, err := f.GetQuote(ctx, canonical)
	if err != nil {
		return nil, err
	}
	series, err := f.fetchSeries(ctx, base, quote, 365)
	if err != nil {
		// Fall back to the 30d window so the detail screen still has a chart.
		series, _ = f.fetchSeries(ctx, base, quote, 30)
	}
	d := &Detail{Quote: *q, Series: series}
	if len(series) > 0 {
		open := series[0]
		d.Open = &open
		lo, hi := series[0], series[0]
		for _, v := range series {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		d.Week52Low = &lo
		d.Week52High = &hi
	}
	return d, nil
}

var _ AssetProvider = (*Frankfurter)(nil)
