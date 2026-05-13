using System.Net.Http.Json;
using System.Text.Json.Serialization;
using Night.Ms.SshServer.Caching;

namespace Night.Ms.SshServer.Providers;

// Free, key-less news provider backed by https://hacker-news.firebaseio.com. Fits the BBS
// aesthetic — programmer news, simple data shape. The official API returns IDs from the
// top-stories list; we fetch the first N items in parallel and turn them into headlines.
// Cached for 5 minutes (per requested max) so opening the NewsScreen back-to-back doesn't
// burst API traffic.
public sealed class HackerNewsProvider(IHttpClientFactory httpClientFactory, ILogger<HackerNewsProvider> logger)
    : INewsProvider
{
    public static readonly TimeSpan CacheTtl = TimeSpan.FromMinutes(5);
    public const string HttpClientName = "hacker-news";

    // Keyed by the clamped max so different request sizes don't share an entry. In practice
    // every caller uses the same max (15 from NewsScreen), so there's one live entry.
    private readonly TtlAsyncCache<int, IReadOnlyList<NewsHeadline>> _cache = new(CacheTtl);

    public async Task<IReadOnlyList<NewsHeadline>> GetTopAsync(int max, CancellationToken cancellationToken = default)
    {
        max = Math.Clamp(max, 1, 30);

        if (_cache.TryGetFresh(max, out var cached)) return cached;

        try
        {
            var fresh = await FetchAsync(max, cancellationToken).ConfigureAwait(false);
            _cache.Set(max, fresh);
            return fresh;
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "HN fetch failed; returning last cached headlines if any.");
            return _cache.TryGetAny(max, out var stale) ? stale : [];
        }
    }

    private async Task<IReadOnlyList<NewsHeadline>> FetchAsync(int max, CancellationToken ct)
    {
        var http = httpClientFactory.CreateClient(HttpClientName);
        var ids = await http.GetFromJsonAsync<int[]>("v0/topstories.json", SnakeCaseJson.Options, ct).ConfigureAwait(false)
                  ?? [];

        var picks = ids.Take(max).ToArray();
        var fetched = await Task.WhenAll(picks.Select(id => FetchItemAsync(http, id, ct))).ConfigureAwait(false);
        return fetched.Where(h => h is not null).Select(h => h!).ToList();
    }

    private static async Task<NewsHeadline?> FetchItemAsync(HttpClient http, int id, CancellationToken ct)
    {
        try
        {
            var item = await http.GetFromJsonAsync<HnItem>($"v0/item/{id}.json", SnakeCaseJson.Options, ct).ConfigureAwait(false);
            if (item?.Title is null) return null;
            return new NewsHeadline(
                Title: item.Title,
                Url: item.Url,
                Author: item.By,
                Score: item.Score,
                PublishedAt: DateTimeOffset.FromUnixTimeSeconds(item.Time));
        }
        catch
        {
            return null;
        }
    }

    private sealed record HnItem(
        [property: JsonPropertyName("id")] int Id,
        [property: JsonPropertyName("title")] string? Title,
        [property: JsonPropertyName("url")] string? Url,
        [property: JsonPropertyName("by")] string? By,
        [property: JsonPropertyName("score")] int? Score,
        [property: JsonPropertyName("time")] long Time);
}
