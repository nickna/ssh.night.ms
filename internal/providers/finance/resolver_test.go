package finance

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct {
		in       string
		wantKind Kind
		wantCan  string
		wantDisp string
		wantErr  bool
	}{
		// Stocks (default + prefix)
		{"AAPL", KindStock, "AAPL", "AAPL", false},
		{"aapl", KindStock, "AAPL", "AAPL", false},
		{"s:nvda", KindStock, "NVDA", "NVDA", false},
		{"S:META", KindStock, "META", "META", false},

		// Known cryptos (auto-detect + prefix + raw id)
		{"BTC", KindCrypto, "bitcoin", "BTC", false},
		{"btc", KindCrypto, "bitcoin", "BTC", false},
		{"DOGE", KindCrypto, "dogecoin", "DOGE", false},
		{"c:eth", KindCrypto, "ethereum", "ETH", false},
		{"c:thefakecoin", KindCrypto, "thefakecoin", "THEFAKECOIN", false},

		// FX (with separators + concat + prefix)
		{"EUR/USD", KindFx, "EURUSD", "EUR/USD", false},
		{"EUR:USD", KindFx, "EURUSD", "EUR/USD", false},
		{"EUR-USD", KindFx, "EURUSD", "EUR/USD", false},
		{"eurusd", KindFx, "EURUSD", "EUR/USD", false},
		{"EURUSD", KindFx, "EURUSD", "EUR/USD", false},
		{"fx:gbp/jpy", KindFx, "GBPJPY", "GBP/JPY", false},
		{"fx:gbpjpy", KindFx, "GBPJPY", "GBP/JPY", false},

		// 4-letter stock with hyphen-like separator should NOT be FX
		{"BRK-B", KindStock, "BRK-B", "BRK-B", false},

		// Errors
		{"", 0, "", "", true},
		{"fx:EUR", 0, "", "", true},
		{"fx:EURUSDX", 0, "", "", true},
		{"s:", 0, "", "", true},
		{"c:", 0, "", "", true},
	}
	for _, c := range cases {
		got, err := Resolve(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Resolve(%q): want err, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(%q): unexpected err %v", c.in, err)
			continue
		}
		if got.Kind != c.wantKind || got.Canonical != c.wantCan || got.DisplayHint != c.wantDisp {
			t.Errorf("Resolve(%q) = {%v %q %q}, want {%v %q %q}",
				c.in, got.Kind, got.Canonical, got.DisplayHint,
				c.wantKind, c.wantCan, c.wantDisp)
		}
	}
}

func TestCryptoDisplay(t *testing.T) {
	cases := map[string]string{
		"bitcoin":    "BTC",
		"ethereum":   "ETH",
		"dogecoin":   "DOGE",
		"unknown-id": "UNKNOWN-ID",
	}
	for id, want := range cases {
		if got := CryptoDisplay(id); got != want {
			t.Errorf("CryptoDisplay(%q) = %q, want %q", id, got, want)
		}
	}
}
