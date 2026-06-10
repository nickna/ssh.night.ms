// Package finance defines the quote provider contract + backend impls
// (CoinGecko for crypto, Yahoo for stocks, Frankfurter for FX).
package finance

import (
	"context"
	"time"
)

// Kind discriminates a watchlist row's asset class. Stored as int32 in
// user_watchlist_items.kind so the values must stay stable.
type Kind int32

const (
	KindStock  Kind = 0
	KindCrypto Kind = 1
	KindFx     Kind = 2
)

func (k Kind) String() string {
	switch k {
	case KindStock:
		return "stock"
	case KindCrypto:
		return "crypto"
	case KindFx:
		return "fx"
	}
	return "?"
}

// Quote is a single market data point.
//
// Symbol is the canonical upstream id (e.g. "bitcoin" / "AAPL" / "EURUSD");
// Display is the short ticker we render in the table (e.g. "BTC" / "AAPL" /
// "EUR/USD"). Currency is the quote currency ("USD" for stock/crypto, the
// quote leg for FX pairs).
type Quote struct {
	Symbol       string
	Display      string
	Name         string // long-form (e.g. "Bitcoin", "Apple Inc.")
	Currency     string
	PriceUSD     float64 // price in Currency (name kept for back-compat with current screen)
	Change24hUSD float64 // delta over the last 24h (in Currency)
	Change24hPct float64 // % over the last 24h
	MarketCapUSD float64 // 0 when unavailable (Yahoo/Frankfurter)
	Last         time.Time
}

// Detail is the deep view of a single instrument, used by FinanceDetailScreen.
// All optional pointers are nil when the upstream doesn't expose the field
// (e.g. CoinGecko has no 52-week range; Frankfurter has no volume).
type Detail struct {
	Quote                // the same fields rendered on the list row
	Open       *float64  // session open (or 1-year open for FX)
	DayLow     *float64  // 24h low
	DayHigh    *float64  // 24h high
	Week52Low  *float64  // 52-week low (stocks only)
	Week52High *float64  // 52-week high (stocks only)
	Volume     *int64    // session volume (stocks only)
	Series     []float64 // chart series — intraday for stocks/crypto, daily for FX
}

// AssetProvider is implemented by a per-asset-class backend (Yahoo for
// stocks, CoinGecko for crypto, Frankfurter for FX). It knows nothing about
// Kind because callers route to a single backend per kind; Multi is the
// kind-aware front-door used by the screen.
type AssetProvider interface {
	GetQuote(ctx context.Context, canonical string) (*Quote, error)
	GetSparkline(ctx context.Context, canonical string) ([]float64, error)
	GetDetail(ctx context.Context, canonical string) (*Detail, error)
}

// Provider is the multi-asset front-door used by the Finance screen — it
// routes each call to the right AssetProvider based on Kind.
type Provider interface {
	GetQuote(ctx context.Context, kind Kind, canonical string) (*Quote, error)
	GetSparkline(ctx context.Context, kind Kind, canonical string) ([]float64, error)
	GetDetail(ctx context.Context, kind Kind, canonical string) (*Detail, error)
}

// Multi routes per-Kind to a concrete AssetProvider. A nil leg returns
// ErrUnsupportedKind so the screen can render a "—" placeholder gracefully
// instead of crashing.
type Multi struct {
	Stock  AssetProvider
	Crypto AssetProvider
	Fx     AssetProvider
}

var ErrUnsupportedKind = errAssetUnsupported{}

type errAssetUnsupported struct{}

func (errAssetUnsupported) Error() string { return "no provider for this asset kind" }

func (m *Multi) leg(kind Kind) (AssetProvider, error) {
	var p AssetProvider
	switch kind {
	case KindStock:
		p = m.Stock
	case KindCrypto:
		p = m.Crypto
	case KindFx:
		p = m.Fx
	default:
		return nil, ErrUnsupportedKind
	}
	if p == nil {
		return nil, ErrUnsupportedKind
	}
	return p, nil
}

func (m *Multi) GetQuote(ctx context.Context, kind Kind, canonical string) (*Quote, error) {
	p, err := m.leg(kind)
	if err != nil {
		return nil, err
	}
	return p.GetQuote(ctx, canonical)
}

func (m *Multi) GetSparkline(ctx context.Context, kind Kind, canonical string) ([]float64, error) {
	p, err := m.leg(kind)
	if err != nil {
		return nil, err
	}
	return p.GetSparkline(ctx, canonical)
}

func (m *Multi) GetDetail(ctx context.Context, kind Kind, canonical string) (*Detail, error) {
	p, err := m.leg(kind)
	if err != nil {
		return nil, err
	}
	return p.GetDetail(ctx, canonical)
}

var _ Provider = (*Multi)(nil)
