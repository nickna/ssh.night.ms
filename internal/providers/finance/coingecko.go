package finance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nickna/ssh.night.ms/internal/providers/httpjson"
)

// CoinGecko fetches crypto quotes from https://api.coingecko.com — free,
// keyless, rate-limited but generous for the BBS's scale. Canonical ids are
// CoinGecko's own ("bitcoin", "ethereum", "monero", …); the resolver maps
// short tickers → ids via KnownCryptoIDs.
type CoinGecko struct {
	HTTPClient *http.Client
}

func NewCoinGecko() *CoinGecko {
	return &CoinGecko{HTTPClient: &http.Client{Timeout: 8 * time.Second}}
}

// cgHeaders identifies the BBS on every CoinGecko request.
var cgHeaders = map[string]string{"User-Agent": "nightms-bbs/1.0"}

// market is the shared decoding shape for the /coins/markets endpoint.
type cgMarket struct {
	ID                string  `json:"id"`
	Symbol            string  `json:"symbol"`
	Name              string  `json:"name"`
	CurrentPrice      float64 `json:"current_price"`
	PriceChange24h    float64 `json:"price_change_24h"`
	PriceChangePct24h float64 `json:"price_change_percentage_24h"`
	MarketCap         float64 `json:"market_cap"`
	High24h           float64 `json:"high_24h"`
	Low24h            float64 `json:"low_24h"`
	LastUpdated       string  `json:"last_updated"`
}

func (p *CoinGecko) fetchMarket(ctx context.Context, id string) (*cgMarket, error) {
	url := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/markets?vs_currency=usd&ids=%s&price_change_percentage=24h",
		id,
	)
	var rows []cgMarket
	if err := httpjson.Get(ctx, p.HTTPClient, url, &rows, cgHeaders); err != nil {
		var se *httpjson.StatusError
		if errors.As(err, &se) && se.Code == 429 {
			return nil, fmt.Errorf("coingecko: rate limited (429) — try again in a moment")
		}
		return nil, fmt.Errorf("coingecko: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("coingecko: id %q not found", id)
	}
	return &rows[0], nil
}

func (p *CoinGecko) marketToQuote(m *cgMarket) *Quote {
	display := CryptoDisplay(m.ID)
	if strings.TrimSpace(display) == "" {
		display = strings.ToUpper(m.Symbol)
	}
	t, _ := time.Parse(time.RFC3339, m.LastUpdated)
	return &Quote{
		Symbol:       m.ID,
		Display:      display,
		Name:         m.Name,
		Currency:     "USD",
		PriceUSD:     m.CurrentPrice,
		Change24hUSD: m.PriceChange24h,
		Change24hPct: m.PriceChangePct24h,
		MarketCapUSD: m.MarketCap,
		Last:         t,
	}
}

func (p *CoinGecko) GetQuote(ctx context.Context, canonical string) (*Quote, error) {
	m, err := p.fetchMarket(ctx, canonical)
	if err != nil {
		return nil, err
	}
	return p.marketToQuote(m), nil
}

func (p *CoinGecko) GetSparkline(ctx context.Context, canonical string) ([]float64, error) {
	return p.fetchSeries(ctx, canonical, "1")
}

func (p *CoinGecko) GetDetail(ctx context.Context, canonical string) (*Detail, error) {
	m, err := p.fetchMarket(ctx, canonical)
	if err != nil {
		return nil, err
	}
	series, _ := p.fetchSeries(ctx, canonical, "1")
	d := &Detail{Quote: *p.marketToQuote(m), Series: series}
	if m.High24h != 0 {
		hi := m.High24h
		d.DayHigh = &hi
	}
	if m.Low24h != 0 {
		lo := m.Low24h
		d.DayLow = &lo
	}
	// CoinGecko's /coins/markets doesn't include session-open; derive it from
	// the first series point so the stats line always has something to show.
	if len(series) > 0 {
		open := series[0]
		d.Open = &open
	}
	return d, nil
}

// fetchSeries pulls the price-only column from /coins/{id}/market_chart.
// `days` is CoinGecko's range param: "1" for intraday, "365" for a year, etc.
func (p *CoinGecko) fetchSeries(ctx context.Context, id, days string) ([]float64, error) {
	url := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/%s/market_chart?vs_currency=usd&days=%s",
		id, days,
	)
	var body struct {
		Prices [][]float64 `json:"prices"`
	}
	if err := httpjson.Get(ctx, p.HTTPClient, url, &body, cgHeaders); err != nil {
		return nil, fmt.Errorf("coingecko series: %w", err)
	}
	out := make([]float64, 0, len(body.Prices))
	for _, p := range body.Prices {
		if len(p) >= 2 {
			out = append(out, p[1])
		}
	}
	return out, nil
}

var _ AssetProvider = (*CoinGecko)(nil)
