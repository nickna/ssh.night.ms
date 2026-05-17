using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Providers.Finance;

namespace Night.Ms.SshServer.Tests.Providers.Finance;

public class YahooFinanceProviderTests
{
    private static YahooFinanceProvider Build(FakeHttpMessageHandler handler) =>
        new(new StaticHttpClientFactory(handler), NullLogger<YahooFinanceProvider>.Instance);

    private const string QuoteJson =
        """
        {"quoteResponse":{"result":[{
          "symbol":"AAPL",
          "shortName":"Apple Inc.",
          "longName":"Apple Inc. (long)",
          "currency":"USD",
          "regularMarketPrice":187.42,
          "regularMarketChange":1.21,
          "regularMarketChangePercent":0.65
        }]}}
        """;

    private const string ChartJson =
        """
        {"chart":{"result":[{
          "meta":{
            "regularMarketOpen":186.21,
            "regularMarketDayLow":185.50,
            "regularMarketDayHigh":188.10,
            "fiftyTwoWeekLow":124.17,
            "fiftyTwoWeekHigh":199.62,
            "regularMarketVolume":41200000
          },
          "indicators":{"quote":[{
            "close":[186.0, 186.5, null, 187.0, 187.4]
          }]}
        }]}}
        """;

    [Fact]
    public async Task GetQuoteAsync_parses_v7_quote_response()
    {
        var handler = new FakeHttpMessageHandler().Route("v7/finance/quote", QuoteJson);
        var sut = Build(handler);

        var quote = await sut.GetQuoteAsync("AAPL", default);

        Assert.NotNull(quote);
        Assert.Equal("AAPL", quote!.Canonical);
        Assert.Equal("Apple Inc. (long)", quote.DisplayName);
        Assert.Equal(187.42m, quote.Price);
        Assert.Equal(1.21m, quote.Change);
        Assert.Equal(0.65m, quote.ChangePct);
        Assert.Equal("USD", quote.Currency);
    }

    [Fact]
    public async Task GetQuoteAsync_caches_within_TTL()
    {
        var handler = new FakeHttpMessageHandler().Route("v7/finance/quote", QuoteJson);
        var sut = Build(handler);

        await sut.GetQuoteAsync("AAPL", default);
        await sut.GetQuoteAsync("AAPL", default);

        Assert.Single(handler.Requests);
    }

    [Fact]
    public async Task GetQuoteAsync_returns_null_when_no_result_row()
    {
        var handler = new FakeHttpMessageHandler().Route("v7/finance/quote", """{"quoteResponse":{"result":[]}}""");
        var sut = Build(handler);

        Assert.Null(await sut.GetQuoteAsync("NOPE", default));
    }

    [Fact]
    public async Task GetQuoteAsync_returns_null_when_upstream_500s()
    {
        var handler = new FakeHttpMessageHandler().Route("v7/finance/quote", "boom", System.Net.HttpStatusCode.InternalServerError);
        var sut = Build(handler);

        Assert.Null(await sut.GetQuoteAsync("AAPL", default));
    }

    [Fact]
    public async Task GetSparklineAsync_returns_closes_with_nulls_carried_forward()
    {
        var handler = new FakeHttpMessageHandler().Route("v8/finance/chart/AAPL", ChartJson);
        var sut = Build(handler);

        var series = await sut.GetSparklineAsync("AAPL", default);

        Assert.NotNull(series);
        // 5 input bars, one null between 186.5 and 187.0 — null is replaced by previous value
        // (186.5) and the series stays length 5.
        Assert.Equal(5, series!.Count);
        Assert.Equal(186.0, series[0]);
        Assert.Equal(186.5, series[2]); // null replaced
        Assert.Equal(187.4, series[^1]);
    }

    [Fact]
    public async Task GetDetailAsync_combines_quote_and_chart_into_stats()
    {
        var handler = new FakeHttpMessageHandler()
            .Route("v7/finance/quote", QuoteJson)
            .Route("v8/finance/chart/AAPL", ChartJson);
        var sut = Build(handler);

        var detail = await sut.GetDetailAsync("AAPL", default);

        Assert.NotNull(detail);
        Assert.Equal(187.42m, detail!.Quote.Price);
        Assert.Equal(186.21m, detail.Open);
        Assert.Equal(185.50m, detail.DayLow);
        Assert.Equal(188.10m, detail.DayHigh);
        Assert.Equal(124.17m, detail.Week52Low);
        Assert.Equal(199.62m, detail.Week52High);
        Assert.Equal(41200000L, detail.Volume);
        Assert.Equal(5, detail.Series.Count);
    }

    [Fact]
    public async Task Builds_correct_quote_url()
    {
        var handler = new FakeHttpMessageHandler().Route("v7/finance/quote", QuoteJson);
        var sut = Build(handler);
        await sut.GetQuoteAsync("BRK.B", default);
        Assert.Contains("symbols=BRK.B", handler.Requests[0].RequestUri!.ToString());
    }
}
