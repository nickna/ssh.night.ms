using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers.Finance;

namespace Night.Ms.SshServer.Tests.Providers.Finance;

public class SymbolResolverTests
{
    [Theory]
    [InlineData("AAPL", WatchlistKind.Stock, "AAPL")]
    [InlineData("aapl", WatchlistKind.Stock, "AAPL")]
    [InlineData("BRK.B", WatchlistKind.Stock, "BRK.B")]
    [InlineData("^GSPC", WatchlistKind.Stock, "^GSPC")]
    [InlineData("META", WatchlistKind.Stock, "META")]
    public void Stocks_resolve_to_uppercase_canonical(string raw, WatchlistKind kind, string canonical)
    {
        var r = SymbolResolver.Resolve(raw);
        Assert.NotNull(r);
        Assert.Equal(kind, r!.Kind);
        Assert.Equal(canonical, r.Canonical);
    }

    [Theory]
    [InlineData("BTC", "bitcoin")]
    [InlineData("btc", "bitcoin")]
    [InlineData("ETH", "ethereum")]
    [InlineData("DOGE", "dogecoin")]
    [InlineData("MATIC", "matic-network")]
    public void Known_crypto_tickers_map_to_coingecko_id(string raw, string canonical)
    {
        var r = SymbolResolver.Resolve(raw);
        Assert.NotNull(r);
        Assert.Equal(WatchlistKind.Crypto, r!.Kind);
        Assert.Equal(canonical, r.Canonical);
    }

    [Theory]
    [InlineData("EUR/USD", "EURUSD")]
    [InlineData("eur/usd", "EURUSD")]
    [InlineData("GBP:JPY", "GBPJPY")]
    public void Fx_pairs_with_separator_auto_resolve(string raw, string canonical)
    {
        var r = SymbolResolver.Resolve(raw);
        Assert.NotNull(r);
        Assert.Equal(WatchlistKind.Fx, r!.Kind);
        Assert.Equal(canonical, r.Canonical);
    }

    [Theory]
    [InlineData("fx:EURUSD", "EURUSD")]
    [InlineData("fx:USD-CAD", "USDCAD")]
    [InlineData("fx:USD:JPY", "USDJPY")]
    public void Fx_prefix_accepts_concatenated_or_separator_form(string raw, string canonical)
    {
        var r = SymbolResolver.Resolve(raw);
        Assert.NotNull(r);
        Assert.Equal(WatchlistKind.Fx, r!.Kind);
        Assert.Equal(canonical, r.Canonical);
    }

    [Fact]
    public void Bare_6_letter_input_resolves_as_stock_not_fx()
    {
        // Without a separator or fx: prefix we keep auto-detect conservative — a 6-letter
        // ticker is more likely a stock (RBLX, NTNX, etc.) than concatenated FX. Users who
        // want the 6-letter FX shorthand use the fx: prefix.
        var r = SymbolResolver.Resolve("EURUSD");
        Assert.NotNull(r);
        Assert.Equal(WatchlistKind.Stock, r!.Kind);
    }

    [Theory]
    [InlineData("s:btc", WatchlistKind.Stock, "BTC")]
    [InlineData("c:doge", WatchlistKind.Crypto, "dogecoin")]
    [InlineData("c:fancyswap", WatchlistKind.Crypto, "fancyswap")]
    [InlineData("fx:eur/usd", WatchlistKind.Fx, "EURUSD")]
    public void Type_prefix_overrides_auto_detection(string raw, WatchlistKind kind, string canonical)
    {
        var r = SymbolResolver.Resolve(raw);
        Assert.NotNull(r);
        Assert.Equal(kind, r!.Kind);
        Assert.Equal(canonical, r.Canonical);
    }

    [Theory]
    [InlineData("")]
    [InlineData("   ")]
    [InlineData(null)]
    public void Empty_input_returns_null(string? raw)
    {
        Assert.Null(SymbolResolver.Resolve(raw));
    }

    [Theory]
    [InlineData("fx:ab")]       // too short to be a currency code
    [InlineData("fx:ABCD/USD")] // base too long
    public void Malformed_fx_returns_null(string raw)
    {
        Assert.Null(SymbolResolver.Resolve(raw));
    }

    [Fact]
    public void Unknown_crypto_ticker_falls_through_to_stock_by_default()
    {
        // FOO isn't in the embedded crypto map. Without a c: prefix it should resolve as a
        // stock — the user can force-crypto-resolve with "c:foo" if they want.
        var r = SymbolResolver.Resolve("FOO");
        Assert.NotNull(r);
        Assert.Equal(WatchlistKind.Stock, r!.Kind);
    }

    [Fact]
    public void DisplayHint_includes_kind_label()
    {
        Assert.Contains("(stock)", SymbolResolver.Resolve("AAPL")!.DisplayHint);
        Assert.Contains("(crypto)", SymbolResolver.Resolve("BTC")!.DisplayHint);
        Assert.Contains("(fx)", SymbolResolver.Resolve("EUR/USD")!.DisplayHint);
    }
}
