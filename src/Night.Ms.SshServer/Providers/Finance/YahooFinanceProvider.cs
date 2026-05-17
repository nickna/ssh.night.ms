using System.Net.Http.Json;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Caching;

namespace Night.Ms.SshServer.Providers.Finance;

// Stock quotes + intraday sparkline + detail panel via Yahoo Finance's unofficial endpoints
// (query1.finance.yahoo.com). No key, no signup, but Yahoo has been aggressively blocking
// scraper-shaped traffic since 2025 — we keep a polite User-Agent and fail gracefully when
// a request is rejected. The composite provider already turns null into a UI "—" cell.
//
// Two endpoints:
//   v7/finance/quote     — batch quote snapshot (price, change, day range, 52w hi/lo, vol)
//   v8/finance/chart     — time-series for the sparkline / detail chart
//
// Caching mirrors HackerNewsProvider: TTL-keyed by the canonical symbol. Quote TTL is short
// (60 s) so users see fresh prices; chart TTL is 5 min since intraday bars don't move that
// often and one user refreshing shouldn't burn the cache for another.
internal sealed class YahooFinanceProvider(
    IHttpClientFactory httpClientFactory,
    ILogger<YahooFinanceProvider> logger) : IStockQuoteProvider
{
    public const string HttpClientName = "yahoo-finance";

    private static readonly TimeSpan QuoteTtl = TimeSpan.FromSeconds(60);
    private static readonly TimeSpan ChartTtl = TimeSpan.FromMinutes(5);

    private readonly TtlAsyncCache<string, FinanceQuote> _quoteCache = new(QuoteTtl);
    private readonly TtlAsyncCache<string, IReadOnlyList<double>> _sparkCache = new(ChartTtl);
    private readonly TtlAsyncCache<string, FinanceDetail> _detailCache = new(ChartTtl);

    public async Task<FinanceQuote?> GetQuoteAsync(string canonical, CancellationToken ct)
    {
        if (_quoteCache.TryGetFresh(canonical, out var hit)) return hit;
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            var url = $"v7/finance/quote?symbols={Uri.EscapeDataString(canonical)}";
            var resp = await http.GetFromJsonAsync<QuoteEnvelope>(url, JsonOpts, ct).ConfigureAwait(false);
            var row = resp?.QuoteResponse?.Result?.FirstOrDefault();
            if (row is null) return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;

            var quote = new FinanceQuote(
                Canonical: canonical,
                DisplayName: row.LongName ?? row.ShortName ?? canonical,
                Price: (decimal)(row.RegularMarketPrice ?? 0d),
                Change: (decimal)(row.RegularMarketChange ?? 0d),
                ChangePct: (decimal)(row.RegularMarketChangePercent ?? 0d),
                Currency: row.Currency ?? "USD",
                AsOf: DateTimeOffset.UtcNow);
            _quoteCache.Set(canonical, quote);
            return quote;
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "Yahoo quote failed for {Symbol}", canonical);
            return _quoteCache.TryGetAny(canonical, out var stale) ? stale : null;
        }
    }

    public async Task<IReadOnlyList<double>?> GetSparklineAsync(string canonical, CancellationToken ct)
    {
        if (_sparkCache.TryGetFresh(canonical, out var hit)) return hit;
        var series = await FetchChartAsync(canonical, range: "1d", interval: "5m", ct).ConfigureAwait(false);
        if (series is null) return _sparkCache.TryGetAny(canonical, out var stale) ? stale : null;
        _sparkCache.Set(canonical, series);
        return series;
    }

    public async Task<FinanceDetail?> GetDetailAsync(string canonical, CancellationToken ct)
    {
        if (_detailCache.TryGetFresh(canonical, out var hit)) return hit;
        var quoteTask = GetQuoteAsync(canonical, ct);
        var chartTask = FetchChartWithMetaAsync(canonical, range: "1d", interval: "5m", ct);
        await Task.WhenAll(quoteTask, chartTask).ConfigureAwait(false);
        var quote = quoteTask.Result;
        var chart = chartTask.Result;
        if (quote is null) return _detailCache.TryGetAny(canonical, out var stale) ? stale : null;
        var detail = new FinanceDetail(
            Quote: quote,
            DayLow: chart?.DayLow,
            DayHigh: chart?.DayHigh,
            Week52Low: chart?.Week52Low,
            Week52High: chart?.Week52High,
            Open: chart?.Open,
            Volume: chart?.Volume,
            Series: chart?.Series ?? []);
        _detailCache.Set(canonical, detail);
        return detail;
    }

    private async Task<IReadOnlyList<double>?> FetchChartAsync(string canonical, string range, string interval, CancellationToken ct)
    {
        var chart = await FetchChartWithMetaAsync(canonical, range, interval, ct).ConfigureAwait(false);
        return chart?.Series;
    }

    private async Task<ChartResult?> FetchChartWithMetaAsync(string canonical, string range, string interval, CancellationToken ct)
    {
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            var url = $"v8/finance/chart/{Uri.EscapeDataString(canonical)}?range={range}&interval={interval}";
            var resp = await http.GetFromJsonAsync<ChartEnvelope>(url, JsonOpts, ct).ConfigureAwait(false);
            var row = resp?.Chart?.Result?.FirstOrDefault();
            var closes = row?.Indicators?.Quote?.FirstOrDefault()?.Close;
            if (closes is null || closes.Length == 0) return null;
            // Closes can contain nulls when the bar didn't trade. Replace with the previous
            // non-null value so the chart renders continuously.
            var clean = new List<double>(closes.Length);
            var last = 0d;
            var seen = false;
            foreach (var v in closes)
            {
                if (v is { } d) { last = d; seen = true; clean.Add(d); }
                else if (seen) clean.Add(last);
            }
            return new ChartResult(
                Series: clean,
                Open: row?.Meta?.RegularMarketOpen is { } o ? (decimal)o : null,
                DayLow: row?.Meta?.RegularMarketDayLow is { } dl ? (decimal)dl : null,
                DayHigh: row?.Meta?.RegularMarketDayHigh is { } dh ? (decimal)dh : null,
                Week52Low: row?.Meta?.FiftyTwoWeekLow is { } wl ? (decimal)wl : null,
                Week52High: row?.Meta?.FiftyTwoWeekHigh is { } wh ? (decimal)wh : null,
                Volume: row?.Meta?.RegularMarketVolume);
        }
        catch (Exception ex)
        {
            logger.LogDebug(ex, "Yahoo chart failed for {Symbol}", canonical);
            return null;
        }
    }

    private static readonly JsonSerializerOptions JsonOpts = new()
    {
        PropertyNameCaseInsensitive = true,
    };

    private sealed record ChartResult(
        IReadOnlyList<double> Series,
        decimal? Open,
        decimal? DayLow,
        decimal? DayHigh,
        decimal? Week52Low,
        decimal? Week52High,
        long? Volume);

    private sealed record QuoteEnvelope(
        [property: JsonPropertyName("quoteResponse")] QuoteResponse? QuoteResponse);
    private sealed record QuoteResponse(
        [property: JsonPropertyName("result")] QuoteRow[]? Result);
    private sealed record QuoteRow(
        [property: JsonPropertyName("symbol")] string Symbol,
        [property: JsonPropertyName("shortName")] string? ShortName,
        [property: JsonPropertyName("longName")] string? LongName,
        [property: JsonPropertyName("currency")] string? Currency,
        [property: JsonPropertyName("regularMarketPrice")] double? RegularMarketPrice,
        [property: JsonPropertyName("regularMarketChange")] double? RegularMarketChange,
        [property: JsonPropertyName("regularMarketChangePercent")] double? RegularMarketChangePercent);

    private sealed record ChartEnvelope(
        [property: JsonPropertyName("chart")] ChartResponse? Chart);
    private sealed record ChartResponse(
        [property: JsonPropertyName("result")] ChartRow[]? Result);
    private sealed record ChartRow(
        [property: JsonPropertyName("meta")] ChartMeta? Meta,
        [property: JsonPropertyName("indicators")] ChartIndicators? Indicators);
    private sealed record ChartMeta(
        [property: JsonPropertyName("regularMarketOpen")] double? RegularMarketOpen,
        [property: JsonPropertyName("regularMarketDayLow")] double? RegularMarketDayLow,
        [property: JsonPropertyName("regularMarketDayHigh")] double? RegularMarketDayHigh,
        [property: JsonPropertyName("fiftyTwoWeekLow")] double? FiftyTwoWeekLow,
        [property: JsonPropertyName("fiftyTwoWeekHigh")] double? FiftyTwoWeekHigh,
        [property: JsonPropertyName("regularMarketVolume")] long? RegularMarketVolume);
    private sealed record ChartIndicators(
        [property: JsonPropertyName("quote")] ChartQuote[]? Quote);
    private sealed record ChartQuote(
        [property: JsonPropertyName("close")] double?[]? Close);
}

public static class YahooFinanceProviderRegistration
{
    public static IServiceCollection AddYahooFinance(this IServiceCollection services)
    {
        services.AddHttpClient(YahooFinanceProvider.HttpClientName, c =>
        {
            c.BaseAddress = new Uri("https://query1.finance.yahoo.com/");
            // Yahoo blocks the default .NET User-Agent. A real-browser-shaped UA keeps the
            // unofficial endpoints reachable; if/when this stops working, the Stooq fallback
            // is a one-file swap behind IStockQuoteProvider.
            c.DefaultRequestHeaders.UserAgent.ParseAdd("Mozilla/5.0 (compatible; nightms-bbs/1.0; +https://night.ms)");
            c.Timeout = TimeSpan.FromSeconds(10);
        });
        services.AddSingleton<IStockQuoteProvider, YahooFinanceProvider>();
        return services;
    }
}
