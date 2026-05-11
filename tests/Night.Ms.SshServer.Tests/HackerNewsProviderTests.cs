using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Providers;

namespace Night.Ms.SshServer.Tests;

public class HackerNewsProviderTests
{
    // Helper: routes /v0/topstories.json + /v0/item/{id}.json fetches against a fake handler.
    private static FakeHttpMessageHandler BuildHandler(int[] topIds, Dictionary<int, string?> itemTitles)
    {
        var handler = new FakeHttpMessageHandler();
        handler.Route("topstories.json", $"[{string.Join(",", topIds)}]");
        foreach (var id in topIds)
        {
            var title = itemTitles.TryGetValue(id, out var t) ? t : $"story {id}";
            var json = title is null
                ? "{\"id\":" + id + "}"  // missing title — provider should skip
                : "{\"id\":" + id + ",\"title\":\"" + title + "\",\"url\":\"https://example.com/" + id + "\",\"by\":\"author" + id + "\",\"score\":" + (id * 10) + ",\"time\":1700000000}";
            handler.Route($"item/{id}.json", json);
        }
        return handler;
    }

    private static HackerNewsProvider Build(FakeHttpMessageHandler handler) =>
        new(new StaticHttpClientFactory(handler), NullLogger<HackerNewsProvider>.Instance);

    [Fact]
    public async Task Returns_top_N_headlines_in_order()
    {
        var handler = BuildHandler([11, 22, 33], new() { [11] = "first", [22] = "second", [33] = "third" });
        var sut = Build(handler);

        var headlines = await sut.GetTopAsync(3);

        Assert.Equal(3, headlines.Count);
        Assert.Equal("first", headlines[0].Title);
        Assert.Equal("https://example.com/11", headlines[0].Url);
        Assert.Equal("author11", headlines[0].Author);
        Assert.Equal(110, headlines[0].Score);
    }

    [Fact]
    public async Task Caps_request_count_at_max_argument()
    {
        var handler = BuildHandler([1, 2, 3, 4, 5], new());
        var sut = Build(handler);

        var headlines = await sut.GetTopAsync(2);

        Assert.Equal(2, headlines.Count);
        // Top-stories endpoint plus the two item fetches we actually used.
        var itemRequests = handler.Requests.Count(r => r.RequestUri!.ToString().Contains("item/"));
        Assert.Equal(2, itemRequests);
    }

    [Fact]
    public async Task Skips_items_with_no_title()
    {
        var handler = BuildHandler([1, 2, 3], new() { [1] = "ok", [2] = null, [3] = "ok-too" });
        var sut = Build(handler);

        var headlines = await sut.GetTopAsync(3);

        Assert.Equal(2, headlines.Count);
        Assert.DoesNotContain(headlines, h => h.Title == "story 2");
    }

    [Fact]
    public async Task Caches_subsequent_calls_within_TTL()
    {
        var handler = BuildHandler([1, 2], new() { [1] = "a", [2] = "b" });
        var sut = Build(handler);

        var first = await sut.GetTopAsync(2);
        var firstCount = handler.Requests.Count;
        var second = await sut.GetTopAsync(2);

        Assert.Equal(firstCount, handler.Requests.Count);
        Assert.Equal(first[0].Title, second[0].Title);
    }

    [Fact]
    public async Task Returns_empty_when_topstories_fetch_throws_and_no_cache_exists()
    {
        var handler = new FakeHttpMessageHandler().RouteThrowing("topstories.json", new HttpRequestException("DNS fail"));
        var sut = Build(handler);

        var headlines = await sut.GetTopAsync(5);

        Assert.Empty(headlines);
    }

    [Fact]
    public async Task PublishedAt_decoded_from_unix_seconds()
    {
        var handler = BuildHandler([1], new() { [1] = "ok" });
        var sut = Build(handler);

        var headlines = await sut.GetTopAsync(1);

        // time:1700000000 ≈ 2023-11-14T22:13:20Z
        Assert.Equal(new DateTimeOffset(2023, 11, 14, 22, 13, 20, TimeSpan.Zero), headlines[0].PublishedAt);
    }
}
