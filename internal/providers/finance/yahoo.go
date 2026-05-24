package finance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Yahoo fetches stock quotes via Yahoo Finance's unofficial endpoints —
// query1.finance.yahoo.com/v7/finance/quote for the snapshot and
// /v8/finance/chart for the intraday series and meta block (Open / Day
// hi-lo / 52-week / Volume). No key required, but Yahoo blocks bare clients;
// a real-browser UA keeps the endpoints reachable.
type Yahoo struct {
	HTTPClient *http.Client
}

func NewYahoo() *Yahoo {
	return &Yahoo{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

const yahooUA = "Mozilla/5.0 (compatible; nightms-bbs/1.0; +https://night.ms)"

func (y *Yahoo) httpGet(ctx context.Context, urlStr string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", yahooUA)
	resp, err := y.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("yahoo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return fmt.Errorf("yahoo: rate limited (429)")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("yahoo: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type yQuoteRow struct {
	Symbol                     string  `json:"symbol"`
	ShortName                  string  `json:"shortName"`
	LongName                   string  `json:"longName"`
	Currency                   string  `json:"currency"`
	RegularMarketPrice         float64 `json:"regularMarketPrice"`
	RegularMarketChange        float64 `json:"regularMarketChange"`
	RegularMarketChangePercent float64 `json:"regularMarketChangePercent"`
}

type yQuoteEnvelope struct {
	QuoteResponse struct {
		Result []yQuoteRow `json:"result"`
	} `json:"quoteResponse"`
}

func (y *Yahoo) GetQuote(ctx context.Context, canonical string) (*Quote, error) {
	u := "https://query1.finance.yahoo.com/v7/finance/quote?symbols=" + url.QueryEscape(canonical)
	var env yQuoteEnvelope
	if err := y.httpGet(ctx, u, &env); err != nil {
		return nil, err
	}
	if len(env.QuoteResponse.Result) == 0 {
		return nil, fmt.Errorf("yahoo: symbol %q not found", canonical)
	}
	r := env.QuoteResponse.Result[0]
	name := r.LongName
	if name == "" {
		name = r.ShortName
	}
	if name == "" {
		name = canonical
	}
	currency := r.Currency
	if currency == "" {
		currency = "USD"
	}
	return &Quote{
		Symbol:       canonical,
		Display:      canonical,
		Name:         name,
		Currency:     currency,
		PriceUSD:     r.RegularMarketPrice,
		Change24hUSD: r.RegularMarketChange,
		Change24hPct: r.RegularMarketChangePercent,
		Last:         time.Now(),
	}, nil
}

type yChartMeta struct {
	RegularMarketOpen    *float64 `json:"regularMarketOpen"`
	RegularMarketDayLow  *float64 `json:"regularMarketDayLow"`
	RegularMarketDayHigh *float64 `json:"regularMarketDayHigh"`
	FiftyTwoWeekLow      *float64 `json:"fiftyTwoWeekLow"`
	FiftyTwoWeekHigh     *float64 `json:"fiftyTwoWeekHigh"`
	RegularMarketVolume  *int64   `json:"regularMarketVolume"`
}

type yChartRow struct {
	Meta       *yChartMeta `json:"meta"`
	Indicators struct {
		Quote []struct {
			Close []*float64 `json:"close"`
		} `json:"quote"`
	} `json:"indicators"`
}

type yChartEnvelope struct {
	Chart struct {
		Result []yChartRow `json:"result"`
	} `json:"chart"`
}

// chart returns (series, meta, error). Close-bar nulls are forward-filled so
// the rendered chart reads as a continuous line instead of a gap.
func (y *Yahoo) chart(ctx context.Context, canonical, rang, interval string) ([]float64, *yChartMeta, error) {
	u := fmt.Sprintf(
		"https://query1.finance.yahoo.com/v8/finance/chart/%s?range=%s&interval=%s",
		url.PathEscape(canonical), rang, interval,
	)
	var env yChartEnvelope
	if err := y.httpGet(ctx, u, &env); err != nil {
		return nil, nil, err
	}
	if len(env.Chart.Result) == 0 {
		return nil, nil, fmt.Errorf("yahoo chart: no result for %q", canonical)
	}
	row := env.Chart.Result[0]
	var closes []*float64
	if len(row.Indicators.Quote) > 0 {
		closes = row.Indicators.Quote[0].Close
	}
	series := make([]float64, 0, len(closes))
	var last float64
	var seen bool
	for _, c := range closes {
		if c != nil {
			last = *c
			seen = true
			series = append(series, *c)
		} else if seen {
			series = append(series, last)
		}
	}
	return series, row.Meta, nil
}

func (y *Yahoo) GetSparkline(ctx context.Context, canonical string) ([]float64, error) {
	series, _, err := y.chart(ctx, canonical, "1d", "5m")
	return series, err
}

func (y *Yahoo) GetDetail(ctx context.Context, canonical string) (*Detail, error) {
	q, err := y.GetQuote(ctx, canonical)
	if err != nil {
		return nil, err
	}
	series, meta, _ := y.chart(ctx, canonical, "1d", "5m")
	d := &Detail{Quote: *q, Series: series}
	if meta != nil {
		d.Open = meta.RegularMarketOpen
		d.DayLow = meta.RegularMarketDayLow
		d.DayHigh = meta.RegularMarketDayHigh
		d.Week52Low = meta.FiftyTwoWeekLow
		d.Week52High = meta.FiftyTwoWeekHigh
		d.Volume = meta.RegularMarketVolume
	}
	return d, nil
}

var _ AssetProvider = (*Yahoo)(nil)
