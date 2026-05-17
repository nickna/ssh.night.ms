using System.Globalization;
using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Caching;

namespace Night.Ms.SshServer.Providers.Finance;

// FX rates via Frankfurter (https://frankfurter.dev). ECB reference rates, ~30 currencies,
// daily granularity, no key, no signup. Canonical comes in as "EURUSD" (base + quote
// concatenated by SymbolResolver); we split that into base + quote, call /v1/latest with
// base=EUR&symbols=USD, and compute change vs. yesterday with the historical endpoint.
//
// Because the underlying data is daily, the sparkline is the last 30 days of EOD points
// rather than an intraday line. The detail chart uses the same series.
internal sealed class FrankfurterProvider(
    IHttpClientFactory httpClientFactory,
    ILogger<FrankfurterProvider> logger) : IFxQuoteProvider
{
    public const string HttpClientName = "frankfurter";

    private static readonly TimeSpan QuoteTtl = TimeSpan.FromMinutes(5);
    private static readonly TimeSpan ChartTtl = TimeSpan.FromMinutes(30);

    private readonly TtlAsyncCache<string, FinanceQuote> _quoteCache = new(QuoteTtl);
    private readonly TtlAsyncCache<string, IReadOnlyList<double>> _sparkCache = new(ChartTtl);
    private readonly TtlAsyncCache<string, FinanceDetail> _detailCache = new(ChartTtl);

    public async Task<FinanceQuote?> GetQuoteAsync(string canonical, CancellationToken ct)
    {
        if (_quoteCache.TryGetFresh(canonical, out var hit)) return hit;
        var pair = SplitPair(canonical);
        if (pair is null) return null;
        var (b, q) = pair.Value;

        try
        {
            var series = await FetchSeriesAsync(b, q, days: 30, ct).ConfigureAwait(false);
            if (series is null || series.Count < 1)
                return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;

            var price = (decimal)series[^1];
            var prev = series.Count >= 2 ? (decimal)series[^2] : price;
            var change = price - prev;
            var changePct = prev == 0m ? 0m : (change / prev) * 100m;

            var quote = new FinanceQuote(
                Canonical: canonical,
                DisplayName: $"{b}/{q}",
                Price: price,
                Change: change,
                ChangePct: changePct,
                Currency: q,
                AsOf: DateTimeOffset.UtcNow);
            _quoteCache.Set(canonical, quote);
            return quote;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "Frankfurter quote failed for {Pair}", canonical);
            return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;
        }
    }

    public async Task<IReadOnlyList<double>?> GetSparklineAsync(string canonical, CancellationToken ct)
    {
        if (_sparkCache.TryGetFresh(canonical, out var hit)) return hit;
        var pair = SplitPair(canonical);
        if (pair is null) return null;
        var (b, q) = pair.Value;
        try
        {
            var series = await FetchSeriesAsync(b, q, days: 30, ct).ConfigureAwait(false);
            if (series is null) return _sparkCache.TryGetAny(canonical, out var stale) ? stale : null;
            _sparkCache.Set(canonical, series);
            return series;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "Frankfurter sparkline failed for {Pair}", canonical);
            return _sparkCache.TryGetAny(canonical, out var stale) ? stale : null;
        }
    }

    public async Task<FinanceDetail?> GetDetailAsync(string canonical, CancellationToken ct)
    {
        if (_detailCache.TryGetFresh(canonical, out var hit)) return hit;
        var pair = SplitPair(canonical);
        if (pair is null) return null;
        var (b, q) = pair.Value;
        var quote = await GetQuoteAsync(canonical, ct).ConfigureAwait(false);
        if (quote is null) return _detailCache.TryGetAny(canonical, out var stale) ? stale : null;
        // 365-day series for the detail screen (still daily granularity); falls back to 30d
        // window cached by the sparkline path if the larger fetch fails.
        var series = await FetchSeriesAsync(b, q, days: 365, ct).ConfigureAwait(false)
                     ?? await GetSparklineAsync(canonical, ct).ConfigureAwait(false);
        decimal? low = null, high = null, open = null;
        if (series is { Count: > 0 })
        {
            low = (decimal)series.Min();
            high = (decimal)series.Max();
            open = (decimal)series[0];
        }
        var detail = new FinanceDetail(
            Quote: quote,
            DayLow: null,
            DayHigh: null,
            Week52Low: low,
            Week52High: high,
            Open: open,
            Volume: null,
            Series: series ?? []);
        _detailCache.Set(canonical, detail);
        return detail;
    }

    private async Task<IReadOnlyList<double>?> FetchSeriesAsync(string baseCcy, string quoteCcy, int days, CancellationToken ct)
    {
        var http = httpClientFactory.CreateClient(HttpClientName);
        var to = DateTime.UtcNow.Date;
        var from = to.AddDays(-days);
        var url = $"v1/{from:yyyy-MM-dd}..{to:yyyy-MM-dd}?base={baseCcy}&symbols={quoteCcy}";
        using var resp = await http.GetAsync(url, ct).ConfigureAwait(false);
        if (!resp.IsSuccessStatusCode) return null;
        await using var s = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        using var doc = await JsonDocument.ParseAsync(s, cancellationToken: ct).ConfigureAwait(false);
        if (!doc.RootElement.TryGetProperty("rates", out var rates)) return null;

        // /v1/{from}..{to} returns { "rates": { "YYYY-MM-DD": { "USD": 1.0842 }, ... } }.
        // Sorted by date ascending so the last entry is the most recent.
        var values = new List<(DateTime Date, double Rate)>();
        foreach (var date in rates.EnumerateObject())
        {
            if (!DateTime.TryParseExact(date.Name, "yyyy-MM-dd", CultureInfo.InvariantCulture, DateTimeStyles.None, out var d))
                continue;
            if (!date.Value.TryGetProperty(quoteCcy, out var r)) continue;
            values.Add((d, r.GetDouble()));
        }
        if (values.Count == 0) return null;
        values.Sort((a, b) => a.Date.CompareTo(b.Date));
        return values.Select(v => v.Rate).ToList();
    }

    private static (string Base, string Quote)? SplitPair(string canonical)
    {
        var s = canonical.Trim().ToUpperInvariant();
        // SymbolResolver hands us 6-char concatenated codes ("EURUSD"). Accept "EUR/USD"
        // too in case a caller bypasses the resolver.
        if (s.Length == 7 && s[3] == '/') return (s[..3], s[4..]);
        if (s.Length == 6) return (s[..3], s[3..]);
        return null;
    }
}

public static class FrankfurterProviderRegistration
{
    public static IServiceCollection AddFrankfurter(this IServiceCollection services)
    {
        services.AddHttpClient(FrankfurterProvider.HttpClientName, c =>
        {
            c.BaseAddress = new Uri("https://api.frankfurter.dev/");
            c.DefaultRequestHeaders.UserAgent.ParseAdd("nightms-bbs/1.0 (+https://night.ms)");
            c.Timeout = TimeSpan.FromSeconds(10);
        });
        services.AddSingleton<IFxQuoteProvider, FrankfurterProvider>();
        return services;
    }
}
