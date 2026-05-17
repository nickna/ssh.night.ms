using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Caching;

namespace Night.Ms.SshServer.Providers.Finance;

// Crypto quotes + intraday sparkline via the CoinGecko public API. No key required, but the
// unauthenticated tier is rate-limited around 5–15 req/min, which is why everything is
// cached for 60 s (quotes) / 5 min (sparkline + detail).
//
// The composite provider passes us a Canonical that's usually the CoinGecko id directly
// ("bitcoin", "ethereum") because SymbolResolver maps the top 20 tickers up front. For
// anything else, this provider falls back to /coins/list (cached 24h) to translate the
// symbol to an id, then proceeds normally.
internal sealed class CoinGeckoProvider(
    IHttpClientFactory httpClientFactory,
    ILogger<CoinGeckoProvider> logger) : ICryptoQuoteProvider
{
    public const string HttpClientName = "coingecko";

    private static readonly TimeSpan QuoteTtl = TimeSpan.FromSeconds(60);
    private static readonly TimeSpan ChartTtl = TimeSpan.FromMinutes(5);
    private static readonly TimeSpan IdMapTtl = TimeSpan.FromHours(24);

    private readonly TtlAsyncCache<string, FinanceQuote> _quoteCache = new(QuoteTtl);
    private readonly TtlAsyncCache<string, IReadOnlyList<double>> _sparkCache = new(ChartTtl);
    private readonly TtlAsyncCache<string, FinanceDetail> _detailCache = new(ChartTtl);
    private readonly TtlAsyncCache<string, IReadOnlyDictionary<string, string>> _idMapCache = new(IdMapTtl);

    public async Task<FinanceQuote?> GetQuoteAsync(string canonical, CancellationToken ct)
    {
        if (_quoteCache.TryGetFresh(canonical, out var hit)) return hit;
        try
        {
            var id = await ResolveIdAsync(canonical, ct).ConfigureAwait(false);
            if (id is null) return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;

            var http = httpClientFactory.CreateClient(HttpClientName);
            var url = $"api/v3/simple/price?ids={Uri.EscapeDataString(id)}&vs_currencies=usd&include_24hr_change=true";
            using var resp = await http.GetAsync(url, ct).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode) return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;
            await using var s = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
            using var doc = await JsonDocument.ParseAsync(s, cancellationToken: ct).ConfigureAwait(false);

            if (!doc.RootElement.TryGetProperty(id, out var row)) return null;
            var price = row.TryGetProperty("usd", out var pp) ? pp.GetDecimal() : 0m;
            var changePct = row.TryGetProperty("usd_24h_change", out var cp) && cp.ValueKind == JsonValueKind.Number
                ? (decimal)cp.GetDouble() : 0m;
            // CoinGecko doesn't directly return absolute 24h change in USD on simple/price.
            // Derive it from the percent: change = price - (price / (1 + pct/100)).
            var prior = changePct == 0m ? price : price / (1m + (changePct / 100m));
            var change = price - prior;

            var quote = new FinanceQuote(
                Canonical: canonical,
                DisplayName: PrettyId(id),
                Price: price,
                Change: change,
                ChangePct: changePct,
                Currency: "USD",
                AsOf: DateTimeOffset.UtcNow);
            _quoteCache.Set(canonical, quote);
            return quote;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "CoinGecko quote failed for {Symbol}", canonical);
            return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;
        }
    }

    public async Task<IReadOnlyList<double>?> GetSparklineAsync(string canonical, CancellationToken ct)
    {
        if (_sparkCache.TryGetFresh(canonical, out var hit)) return hit;
        try
        {
            var id = await ResolveIdAsync(canonical, ct).ConfigureAwait(false);
            if (id is null) return _sparkCache.TryGetAny(canonical, out var stale) ? stale : null;
            var series = await FetchMarketChartAsync(id, days: 1, ct).ConfigureAwait(false);
            if (series is null) return _sparkCache.TryGetAny(canonical, out var stale) ? stale : null;
            _sparkCache.Set(canonical, series);
            return series;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "CoinGecko sparkline failed for {Symbol}", canonical);
            return _sparkCache.TryGetAny(canonical, out var stale) ? stale : null;
        }
    }

    public async Task<FinanceDetail?> GetDetailAsync(string canonical, CancellationToken ct)
    {
        if (_detailCache.TryGetFresh(canonical, out var hit)) return hit;
        var quote = await GetQuoteAsync(canonical, ct).ConfigureAwait(false);
        if (quote is null) return _detailCache.TryGetAny(canonical, out var stale) ? stale : null;
        var id = await ResolveIdAsync(canonical, ct).ConfigureAwait(false);
        var series = id is not null ? await FetchMarketChartAsync(id, days: 1, ct).ConfigureAwait(false) : null;

        decimal? dayLow = null, dayHigh = null;
        if (series is { Count: > 0 })
        {
            dayLow = (decimal)series.Min();
            dayHigh = (decimal)series.Max();
        }

        var detail = new FinanceDetail(
            Quote: quote,
            DayLow: dayLow,
            DayHigh: dayHigh,
            Week52Low: null,   // CoinGecko exposes these on /coins/{id} (not free of extra requests); leave null in v1.
            Week52High: null,
            Open: series is { Count: > 0 } ? (decimal)series[0] : null,
            Volume: null,
            Series: series ?? []);
        _detailCache.Set(canonical, detail);
        return detail;
    }

    private async Task<IReadOnlyList<double>?> FetchMarketChartAsync(string id, int days, CancellationToken ct)
    {
        var http = httpClientFactory.CreateClient(HttpClientName);
        var url = $"api/v3/coins/{Uri.EscapeDataString(id)}/market_chart?vs_currency=usd&days={days}";
        using var resp = await http.GetAsync(url, ct).ConfigureAwait(false);
        if (!resp.IsSuccessStatusCode) return null;
        await using var s = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        using var doc = await JsonDocument.ParseAsync(s, cancellationToken: ct).ConfigureAwait(false);
        if (!doc.RootElement.TryGetProperty("prices", out var prices)) return null;

        var series = new List<double>(prices.GetArrayLength());
        foreach (var pair in prices.EnumerateArray())
        {
            // Each entry is [timestamp_ms, price]. Drop the timestamp; we keep only the price.
            if (pair.ValueKind == JsonValueKind.Array && pair.GetArrayLength() >= 2)
                series.Add(pair[1].GetDouble());
        }
        return series;
    }

    // Best-effort symbol → id translation. The embedded map in SymbolResolver covers the top
    // 20; this method handles the long tail by fetching /coins/list once per day and walking
    // the symbol column. Returns the canonical input unchanged if it already looks like an id
    // (contains a '-' or matches a lowercased id in the map).
    private async Task<string?> ResolveIdAsync(string canonical, CancellationToken ct)
    {
        var lower = canonical.ToLowerInvariant();
        var map = await GetIdMapAsync(ct).ConfigureAwait(false);
        if (map.TryGetValue(lower, out var direct)) return direct;
        // Caller may have passed a ticker we don't know about — fall back to the input so the
        // upstream call still has *something* to try. CoinGecko returns a 404-shaped body on
        // unknown ids which we treat as null at the caller.
        return canonical;
    }

    private async Task<IReadOnlyDictionary<string, string>> GetIdMapAsync(CancellationToken ct)
    {
        const string key = "default";
        if (_idMapCache.TryGetFresh(key, out var hit)) return hit;
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            using var resp = await http.GetAsync("api/v3/coins/list", ct).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode)
                return _idMapCache.TryGetAny(key, out var stale)
                    ? stale
                    : new Dictionary<string, string>();
            await using var s = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
            using var doc = await JsonDocument.ParseAsync(s, cancellationToken: ct).ConfigureAwait(false);
            var map = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            foreach (var el in doc.RootElement.EnumerateArray())
            {
                var id = el.GetProperty("id").GetString();
                var symbol = el.GetProperty("symbol").GetString();
                if (id is null || symbol is null) continue;
                // First occurrence wins so well-known tickers (BTC → bitcoin) aren't shadowed
                // by a meme coin with the same symbol that comes later in the list.
                map.TryAdd(symbol.ToLowerInvariant(), id);
                map.TryAdd(id.ToLowerInvariant(), id);
            }
            _idMapCache.Set(key, map);
            return map;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "CoinGecko /coins/list fetch failed; using cached or empty map.");
            return _idMapCache.TryGetAny(key, out var stale)
                ? stale
                : new Dictionary<string, string>();
        }
    }

    private static string PrettyId(string id) =>
        // "bitcoin" -> "Bitcoin", "matic-network" -> "Matic Network". Display-only.
        string.Join(' ', id.Split('-').Select(p => p.Length > 0 ? char.ToUpperInvariant(p[0]) + p[1..] : p));
}

public static class CoinGeckoProviderRegistration
{
    public static IServiceCollection AddCoinGecko(this IServiceCollection services)
    {
        services.AddHttpClient(CoinGeckoProvider.HttpClientName, c =>
        {
            c.BaseAddress = new Uri("https://api.coingecko.com/");
            c.DefaultRequestHeaders.UserAgent.ParseAdd("nightms-bbs/1.0 (+https://night.ms)");
            c.Timeout = TimeSpan.FromSeconds(10);
        });
        services.AddSingleton<ICryptoQuoteProvider, CoinGeckoProvider>();
        return services;
    }
}
