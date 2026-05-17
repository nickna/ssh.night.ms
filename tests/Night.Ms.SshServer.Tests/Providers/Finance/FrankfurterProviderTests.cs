using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Providers.Finance;

namespace Night.Ms.SshServer.Tests.Providers.Finance;

public class FrankfurterProviderTests
{
    private static FrankfurterProvider Build(FakeHttpMessageHandler handler) =>
        new(new StaticHttpClientFactory(handler), NullLogger<FrankfurterProvider>.Instance);

    // A 5-day range with the latest day showing a small rise — change derives from the last
    // two entries (1.0842 - 1.0824 ≈ +0.0018, ≈ +0.166%).
    private const string RangeJson =
        """
        {"rates":{
          "2026-05-09":{"USD":1.0801},
          "2026-05-10":{"USD":1.0812},
          "2026-05-11":{"USD":1.0820},
          "2026-05-12":{"USD":1.0824},
          "2026-05-13":{"USD":1.0842}
        }}
        """;

    [Fact]
    public async Task GetQuoteAsync_parses_range_and_derives_change()
    {
        var handler = new FakeHttpMessageHandler().Route("v1/", RangeJson);
        var sut = Build(handler);

        var quote = await sut.GetQuoteAsync("EURUSD", default);

        Assert.NotNull(quote);
        Assert.Equal("EUR/USD", quote!.DisplayName);
        Assert.Equal(1.0842m, quote.Price);
        Assert.True(quote.Change > 0m);
        Assert.True(quote.ChangePct > 0m);
        Assert.Equal("USD", quote.Currency);
    }

    [Fact]
    public async Task Builds_correct_url_with_base_and_symbols()
    {
        var handler = new FakeHttpMessageHandler().Route("v1/", RangeJson);
        var sut = Build(handler);

        await sut.GetQuoteAsync("EURUSD", default);

        var url = handler.Requests[0].RequestUri!.ToString();
        Assert.Contains("base=EUR", url);
        Assert.Contains("symbols=USD", url);
        // Date range path is "yyyy-MM-dd..yyyy-MM-dd" — assert the separator presence.
        Assert.Contains("..", url);
    }

    [Fact]
    public async Task GetSparklineAsync_returns_rate_sequence_in_date_order()
    {
        var handler = new FakeHttpMessageHandler().Route("v1/", RangeJson);
        var sut = Build(handler);

        var series = await sut.GetSparklineAsync("EURUSD", default);

        Assert.NotNull(series);
        Assert.Equal(new[] { 1.0801, 1.0812, 1.0820, 1.0824, 1.0842 }, series);
    }

    [Fact]
    public async Task Returns_null_for_malformed_pair()
    {
        var handler = new FakeHttpMessageHandler().Route("v1/", RangeJson);
        var sut = Build(handler);
        Assert.Null(await sut.GetQuoteAsync("ABC", default));
        Assert.Null(await sut.GetQuoteAsync("TOOLONG", default));
    }

    [Fact]
    public async Task Returns_null_when_upstream_500s_and_no_cache_exists()
    {
        var handler = new FakeHttpMessageHandler().Route("v1/", "boom", System.Net.HttpStatusCode.InternalServerError);
        var sut = Build(handler);
        Assert.Null(await sut.GetQuoteAsync("EURUSD", default));
    }

    [Fact]
    public async Task Caches_quote_within_TTL()
    {
        var handler = new FakeHttpMessageHandler().Route("v1/", RangeJson);
        var sut = Build(handler);

        await sut.GetQuoteAsync("EURUSD", default);
        await sut.GetQuoteAsync("EURUSD", default);

        Assert.Single(handler.Requests);
    }
}
