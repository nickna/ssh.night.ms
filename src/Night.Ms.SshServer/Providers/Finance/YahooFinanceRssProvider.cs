using System.Globalization;
using System.Xml.Linq;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Caching;

namespace Night.Ms.SshServer.Providers.Finance;

// Finance news via Yahoo Finance RSS. The headline endpoint accepts a comma-separated
// `s=AAPL,MSFT` query for ticker-filtered news; with an empty list we fall back to broad
// market index news (^DJI,^GSPC) so the news pane is never empty.
//
// RSS 2.0 — parsed with System.Xml.Linq. Cached for 5 minutes keyed by the (ordered, deduped)
// ticker set so two users with the same watchlist share a fetch. Per-user differences just
// produce per-key cache entries; the cardinality is bounded by the small number of distinct
// watchlists on this hobby BBS.
internal sealed class YahooFinanceRssProvider(
    IHttpClientFactory httpClientFactory,
    ILogger<YahooFinanceRssProvider> logger) : IFinanceNewsProvider
{
    public const string HttpClientName = "yahoo-finance-rss";

    private static readonly TimeSpan CacheTtl = TimeSpan.FromMinutes(5);
    // Yahoo's default symbols when no ticker is supplied — broad US market headlines.
    private static readonly string[] DefaultSymbols = ["^DJI", "^GSPC", "^IXIC"];

    private readonly TtlAsyncCache<string, IReadOnlyList<NewsHeadline>> _cache = new(CacheTtl);

    public async Task<IReadOnlyList<NewsHeadline>> GetForTickersAsync(IReadOnlyList<string> tickers, int max, CancellationToken ct = default)
    {
        max = Math.Clamp(max, 1, 30);
        var symbols = NormalizeSymbols(tickers);
        var key = $"{string.Join(',', symbols)}|{max}";

        if (_cache.TryGetFresh(key, out var hit)) return hit;

        try
        {
            var headlines = await FetchAsync(symbols, max, ct).ConfigureAwait(false);
            _cache.Set(key, headlines);
            return headlines;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "Yahoo RSS fetch failed for symbols {Symbols}", string.Join(',', symbols));
            return _cache.TryGetAny(key, out var stale) ? stale : [];
        }
    }

    private static IReadOnlyList<string> NormalizeSymbols(IReadOnlyList<string> tickers)
    {
        if (tickers.Count == 0) return DefaultSymbols;
        // Order-insensitive cache key + de-dup so two equivalent watchlists share an entry.
        // Cap at 10 to keep the URL short and avoid Yahoo silently truncating.
        var distinct = tickers
            .Where(t => !string.IsNullOrWhiteSpace(t))
            .Select(t => t.Trim().ToUpperInvariant())
            .Distinct()
            .OrderBy(t => t, StringComparer.Ordinal)
            .Take(10)
            .ToList();
        return distinct.Count == 0 ? DefaultSymbols : distinct;
    }

    private async Task<IReadOnlyList<NewsHeadline>> FetchAsync(IReadOnlyList<string> symbols, int max, CancellationToken ct)
    {
        var http = httpClientFactory.CreateClient(HttpClientName);
        var url = $"rss/headline?s={Uri.EscapeDataString(string.Join(',', symbols))}";
        using var resp = await http.GetAsync(url, ct).ConfigureAwait(false);
        if (!resp.IsSuccessStatusCode) return [];
        await using var s = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        var doc = await XDocument.LoadAsync(s, LoadOptions.None, ct).ConfigureAwait(false);

        var items = doc.Root?.Element("channel")?.Elements("item") ?? [];
        var headlines = new List<NewsHeadline>();
        foreach (var item in items)
        {
            var title = item.Element("title")?.Value;
            if (string.IsNullOrWhiteSpace(title)) continue;
            var url2 = item.Element("link")?.Value;
            var pubDate = ParsePubDate(item.Element("pubDate")?.Value);
            headlines.Add(new NewsHeadline(
                Title: title.Trim(),
                Url: string.IsNullOrWhiteSpace(url2) ? null : url2.Trim(),
                Author: null,
                Score: null,
                PublishedAt: pubDate));
            if (headlines.Count >= max) break;
        }
        return headlines;
    }

    private static DateTimeOffset ParsePubDate(string? raw)
    {
        if (string.IsNullOrWhiteSpace(raw)) return DateTimeOffset.UtcNow;
        // RSS 2.0 dates follow RFC 822 ("Wed, 14 May 2026 09:12:00 -0400"). DateTimeOffset.Parse
        // handles the common shapes; on parse failure, fall back to "now" so the item still
        // sorts in a reasonable position rather than crashing the feed.
        return DateTimeOffset.TryParse(raw, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var dt)
            ? dt
            : DateTimeOffset.UtcNow;
    }
}

public static class YahooFinanceRssProviderRegistration
{
    public static IServiceCollection AddYahooFinanceNews(this IServiceCollection services)
    {
        services.AddHttpClient(YahooFinanceRssProvider.HttpClientName, c =>
        {
            c.BaseAddress = new Uri("https://finance.yahoo.com/");
            c.DefaultRequestHeaders.UserAgent.ParseAdd("Mozilla/5.0 (compatible; nightms-bbs/1.0; +https://night.ms)");
            c.Timeout = TimeSpan.FromSeconds(10);
        });
        services.AddSingleton<IFinanceNewsProvider, YahooFinanceRssProvider>();
        return services;
    }
}
