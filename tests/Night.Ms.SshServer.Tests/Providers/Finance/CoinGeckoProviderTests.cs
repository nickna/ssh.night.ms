using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Providers.Finance;

namespace Night.Ms.SshServer.Tests.Providers.Finance;

public class CoinGeckoProviderTests
{
    private static CoinGeckoProvider Build(FakeHttpMessageHandler handler) =>
        new(new StaticHttpClientFactory(handler), NullLogger<CoinGeckoProvider>.Instance);

    private const string CoinsListJson =
        """[{"id":"bitcoin","symbol":"btc","name":"Bitcoin"},{"id":"ethereum","symbol":"eth","name":"Ethereum"}]""";

    private const string SimplePriceJson =
        """{"bitcoin":{"usd":98341.0,"usd_24h_change":-0.4521}}""";

    private const string MarketChartJson =
        """{"prices":[[1700000000000,98000.0],[1700003600000,98200.5],[1700007200000,98341.0]]}""";

    [Fact]
    public async Task GetQuoteAsync_uses_canonical_directly_when_it_is_a_coingecko_id()
    {
        var handler = new FakeHttpMessageHandler()
            .Route("coins/list", CoinsListJson)
            .Route("simple/price", SimplePriceJson);
        var sut = Build(handler);

        var quote = await sut.GetQuoteAsync("bitcoin", default);

        Assert.NotNull(quote);
        Assert.Equal(98341.0m, quote!.Price);
        Assert.Equal(-0.4521m, quote.ChangePct);
        // Change is derived from price + pct: price - price/(1 + pct/100). Should be negative
        // since pct is negative, and absolute value is small (sub-1%).
        Assert.True(quote.Change < 0);
        Assert.Equal("USD", quote.Currency);
    }

    [Fact]
    public async Task GetQuoteAsync_caches_within_TTL()
    {
        var handler = new FakeHttpMessageHandler()
            .Route("coins/list", CoinsListJson)
            .Route("simple/price", SimplePriceJson);
        var sut = Build(handler);

        await sut.GetQuoteAsync("bitcoin", default);
        await sut.GetQuoteAsync("bitcoin", default);

        var priceCalls = handler.Requests.Count(r => r.RequestUri!.ToString().Contains("simple/price"));
        Assert.Equal(1, priceCalls);
    }

    [Fact]
    public async Task GetSparklineAsync_returns_price_only_series()
    {
        var handler = new FakeHttpMessageHandler()
            .Route("coins/list", CoinsListJson)
            .Route("market_chart", MarketChartJson);
        var sut = Build(handler);

        var series = await sut.GetSparklineAsync("bitcoin", default);

        Assert.NotNull(series);
        Assert.Equal(new[] { 98000.0, 98200.5, 98341.0 }, series);
    }

    [Fact]
    public async Task GetQuoteAsync_returns_null_when_id_lookup_misses_and_endpoint_404s()
    {
        // /coins/list is provided so the id map cache populates, but bitcoin isn't in it.
        // simple/price for the unknown id returns 404; the provider should soft-fail to null.
        var handler = new FakeHttpMessageHandler()
            .Route("coins/list", "[]")
            .Route("simple/price", "{}", System.Net.HttpStatusCode.NotFound);
        var sut = Build(handler);

        Assert.Null(await sut.GetQuoteAsync("never-heard-of-it", default));
    }

    [Fact]
    public async Task Embedded_ticker_lookup_lowercases_into_id()
    {
        var handler = new FakeHttpMessageHandler()
            .Route("coins/list", CoinsListJson)
            .Route("simple/price", SimplePriceJson);
        var sut = Build(handler);

        await sut.GetQuoteAsync("bitcoin", default);

        var priceUrl = handler.Requests.Single(r => r.RequestUri!.ToString().Contains("simple/price")).RequestUri!.ToString();
        Assert.Contains("ids=bitcoin", priceUrl);
        Assert.Contains("vs_currencies=usd", priceUrl);
    }
}
