package finance

import (
	"errors"
	"fmt"
	"strings"
)

// Resolved is the result of mapping a user-typed symbol to a kind + upstream
// canonical id + the short ticker we render in the SYMBOL column.
type Resolved struct {
	Kind        Kind
	Canonical   string // upstream id ("bitcoin", "AAPL", "EURUSD")
	DisplayHint string // short ticker shown to the user ("BTC", "AAPL", "EUR/USD")
}

// KnownCryptoIDs maps short display tickers (uppercase) to CoinGecko canonical
// ids. Used both by the resolver to detect crypto and by the CoinGecko provider
// to render display tickers. Not exhaustive — anything unknown falls through to
// stock (or the user can force it with the `c:` prefix).
var KnownCryptoIDs = map[string]string{
	"BTC":   "bitcoin",
	"ETH":   "ethereum",
	"XMR":   "monero",
	"SOL":   "solana",
	"ADA":   "cardano",
	"DOT":   "polkadot",
	"LINK":  "chainlink",
	"AVAX":  "avalanche-2",
	"MATIC": "matic-network",
	"DOGE":  "dogecoin",
	"USDT":  "tether",
	"USDC":  "usd-coin",
	"BNB":   "binancecoin",
	"XRP":   "ripple",
	"TRX":   "tron",
	"TON":   "the-open-network",
	"SHIB":  "shiba-inu",
	"LTC":   "litecoin",
	"BCH":   "bitcoin-cash",
	"ATOM":  "cosmos",
}

// CryptoDisplay returns the short ticker for a CoinGecko id, falling back to
// the uppercased id when unknown.
func CryptoDisplay(id string) string {
	for ticker, canon := range KnownCryptoIDs {
		if canon == id {
			return ticker
		}
	}
	return strings.ToUpper(id)
}

// Resolve converts a user-typed symbol into a (kind, canonical, display) tuple.
// The order of rules matters:
//
//  1. Explicit prefix override (`s:`, `c:`, `fx:`) — forces the kind.
//  2. Contains `/`, `:`, or `-` → try parsing as FX pair (EUR/USD, EUR:USD).
//  3. 6-letter all-alpha → FX pair concat form (EURUSD).
//  4. Matches KnownCryptoIDs → crypto.
//  5. Default → stock (uppercase).
//
// Heuristic, no external lookup — mirrors the C# SymbolResolver.
func Resolve(input string) (Resolved, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Resolved{}, errors.New("empty symbol")
	}

	if rest, ok := stripPrefixCI(in, "s:"); ok {
		s := strings.ToUpper(strings.TrimSpace(rest))
		if s == "" {
			return Resolved{}, errors.New("empty stock symbol after s:")
		}
		return Resolved{Kind: KindStock, Canonical: s, DisplayHint: s}, nil
	}
	if rest, ok := stripPrefixCI(in, "c:"); ok {
		s := strings.TrimSpace(rest)
		if s == "" {
			return Resolved{}, errors.New("empty crypto symbol after c:")
		}
		up := strings.ToUpper(s)
		if id, found := KnownCryptoIDs[up]; found {
			return Resolved{Kind: KindCrypto, Canonical: id, DisplayHint: up}, nil
		}
		return Resolved{Kind: KindCrypto, Canonical: strings.ToLower(s), DisplayHint: up}, nil
	}
	if rest, ok := stripPrefixCI(in, "fx:"); ok {
		return parseFX(rest)
	}

	if strings.ContainsAny(in, "/:-") {
		if r, err := parseFX(in); err == nil {
			return r, nil
		}
	}

	if len(in) == 6 && allAlpha(in) {
		return parseFX(in)
	}

	up := strings.ToUpper(in)
	if id, ok := KnownCryptoIDs[up]; ok {
		return Resolved{Kind: KindCrypto, Canonical: id, DisplayHint: up}, nil
	}
	return Resolved{Kind: KindStock, Canonical: up, DisplayHint: up}, nil
}

func parseFX(s string) (Resolved, error) {
	s = strings.TrimSpace(s)
	for _, sep := range []string{"/", ":", "-"} {
		s = strings.ReplaceAll(s, sep, "")
	}
	s = strings.ToUpper(s)
	if len(s) != 6 || !allAlpha(s) {
		return Resolved{}, fmt.Errorf("FX pair %q: expected 6 letters (e.g. EUR/USD)", s)
	}
	from, to := s[:3], s[3:]
	return Resolved{
		Kind:        KindFx,
		Canonical:   from + to,
		DisplayHint: from + "/" + to,
	}, nil
}

// stripPrefixCI is HasPrefix + TrimPrefix in one pass, case-insensitive on the
// prefix. Returns (remainder, true) on match.
func stripPrefixCI(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return s, false
	}
	if !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

func allAlpha(s string) bool {
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}
