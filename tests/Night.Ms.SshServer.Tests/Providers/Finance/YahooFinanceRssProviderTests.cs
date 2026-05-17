using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Providers.Finance;

namespace Night.Ms.SshServer.Tests.Providers.Finance;

public class YahooFinanceRssProviderTests
{
    private static YahooFinanceRssProvider Build(FakeHttpMessageHandler handler) =>
        new(new StaticHttpClientFactory(handler), NullLogger<YahooFinanceRssProvider>.Instance);

    private const string RssXml =
        """
        <?xml version="1.0" encoding="UTF-8"?>
        <rss version="2.0">
          <channel>
            <title>Yahoo Finance</title>
            <item>
              <title>Apple beats Q1 expectations</title>
              <link>https://finance.yahoo.com/news/apple-q1</link>
              <pubDate>Wed, 13 May 2026 09:12:00 -0400</pubDate>
            </item>
            <item>
              <title>Microsoft cloud revenue rises</title>
              <link>https://finance.yahoo.com/news/msft-cloud</link>
              <pubDate>Wed, 13 May 2026 08:30:00 -0400</pubDate>
            </item>
            <item>
              <title>Empty url item</title>
              <link></link>
              <pubDate>Wed, 13 May 2026 07:00:00 -0400</pubDate>
            </item>
          </channel>
        </rss>
        """;

    private static FakeHttpMessageHandler XmlHandler(string body) =>
        new FakeHttpMessageHandler().RouteDynamic("rss/headline", _ => new HttpResponseMessage(System.Net.HttpStatusCode.OK)
        {
            Content = new StringContent(body, System.Text.Encoding.UTF8, "application/xml"),
        });

    [Fact]
    public async Task Parses_rss_items_into_headlines()
    {
        var handler = XmlHandler(RssXml);
        var sut = Build(handler);

        var headlines = await sut.GetForTickersAsync(new[] { "AAPL", "MSFT" }, max: 10, default);

        Assert.Equal(3, headlines.Count);
        Assert.Equal("Apple beats Q1 expectations", headlines[0].Title);
        Assert.Equal("https://finance.yahoo.com/news/apple-q1", headlines[0].Url);
        // Empty <link> becomes null on the headline.
        Assert.Null(headlines[2].Url);
    }

    [Fact]
    public async Task Caps_result_at_max_argument()
    {
        var handler = XmlHandler(RssXml);
        var sut = Build(handler);

        var headlines = await sut.GetForTickersAsync(new[] { "AAPL" }, max: 2, default);

        Assert.Equal(2, headlines.Count);
    }

    [Fact]
    public async Task Empty_ticker_list_falls_back_to_default_market_symbols()
    {
        var handler = XmlHandler(RssXml);
        var sut = Build(handler);

        await sut.GetForTickersAsync(Array.Empty<string>(), max: 5, default);

        var url = handler.Requests[0].RequestUri!.ToString();
        // The provider defaults to ^DJI / ^GSPC / ^IXIC when the ticker list is empty. The
        // '^' may or may not be percent-encoded depending on Uri canonicalization, so just
        // assert the ticker fragments are present somewhere in the query.
        Assert.Contains("DJI", url);
        Assert.Contains("GSPC", url);
        Assert.Contains("IXIC", url);
    }

    [Fact]
    public async Task Same_ticker_set_in_different_order_shares_cache()
    {
        var handler = XmlHandler(RssXml);
        var sut = Build(handler);

        await sut.GetForTickersAsync(new[] { "AAPL", "MSFT" }, max: 5, default);
        await sut.GetForTickersAsync(new[] { "MSFT", "AAPL" }, max: 5, default);

        // Order-insensitive cache key: only one fetch.
        Assert.Single(handler.Requests);
    }

    [Fact]
    public async Task Different_max_uses_separate_cache_entry()
    {
        var handler = XmlHandler(RssXml);
        var sut = Build(handler);

        await sut.GetForTickersAsync(new[] { "AAPL" }, max: 5, default);
        await sut.GetForTickersAsync(new[] { "AAPL" }, max: 3, default);

        Assert.Equal(2, handler.Requests.Count);
    }

    [Fact]
    public async Task Returns_empty_when_upstream_500s_and_no_cache()
    {
        var handler = new FakeHttpMessageHandler().Route("rss/headline", "boom", System.Net.HttpStatusCode.InternalServerError);
        var sut = Build(handler);

        var headlines = await sut.GetForTickersAsync(new[] { "AAPL" }, max: 5, default);

        Assert.Empty(headlines);
    }

    [Fact]
    public async Task Skips_items_without_a_title()
    {
        const string xml = """
            <?xml version="1.0" encoding="UTF-8"?>
            <rss version="2.0">
              <channel>
                <item><title>Only this one</title><link>https://x</link><pubDate>Wed, 13 May 2026 09:00:00 -0400</pubDate></item>
                <item><title></title><link>https://y</link><pubDate>Wed, 13 May 2026 08:00:00 -0400</pubDate></item>
              </channel>
            </rss>
            """;
        var handler = XmlHandler(xml);
        var sut = Build(handler);

        var headlines = await sut.GetForTickersAsync(new[] { "AAPL" }, max: 10, default);

        Assert.Single(headlines);
        Assert.Equal("Only this one", headlines[0].Title);
    }
}
