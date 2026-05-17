using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Providers.Finance;

// A single quoted instrument as shown in the FinanceScreen watchlist row.
//
// Currency is the ISO code the price is denominated in. For Stock + Crypto it's "USD"
// today; for FX it's the quote leg ("USD" in EUR/USD). UI uses it for display formatting,
// not arithmetic.
public sealed record FinanceQuote(
    string Canonical,
    string DisplayName,
    decimal Price,
    decimal Change,
    decimal ChangePct,
    string Currency,
    DateTimeOffset AsOf);

// Drill-in payload: the row's snapshot plus the larger series and stats panel. Any of the
// optional stats may be null when the upstream API doesn't surface that field — the screen
// renders "—" in their place rather than hiding the row.
public sealed record FinanceDetail(
    FinanceQuote Quote,
    decimal? DayLow,
    decimal? DayHigh,
    decimal? Week52Low,
    decimal? Week52High,
    decimal? Open,
    long? Volume,
    IReadOnlyList<double> Series);

// Composite provider used by the screens. The implementation routes by Kind into one of the
// per-source providers (Yahoo / CoinGecko / Frankfurter) registered in DI. Tests can swap a
// fake IFinanceProvider at the composite layer without rebuilding the per-source providers.
public interface IFinanceProvider
{
    Task<FinanceQuote?> GetQuoteAsync(WatchlistKind kind, string canonical, CancellationToken ct = default);
    Task<IReadOnlyList<double>?> GetSparklineAsync(WatchlistKind kind, string canonical, CancellationToken ct = default);
    Task<FinanceDetail?> GetDetailAsync(WatchlistKind kind, string canonical, CancellationToken ct = default);
}

// Per-source providers each implement the same shape. The composite simply dispatches; the
// concrete providers (one per WatchlistKind) live in the same folder.
public interface IStockQuoteProvider
{
    Task<FinanceQuote?> GetQuoteAsync(string canonical, CancellationToken ct);
    Task<IReadOnlyList<double>?> GetSparklineAsync(string canonical, CancellationToken ct);
    Task<FinanceDetail?> GetDetailAsync(string canonical, CancellationToken ct);
}

public interface ICryptoQuoteProvider
{
    Task<FinanceQuote?> GetQuoteAsync(string canonical, CancellationToken ct);
    Task<IReadOnlyList<double>?> GetSparklineAsync(string canonical, CancellationToken ct);
    Task<FinanceDetail?> GetDetailAsync(string canonical, CancellationToken ct);
}

public interface IFxQuoteProvider
{
    Task<FinanceQuote?> GetQuoteAsync(string canonical, CancellationToken ct);
    Task<IReadOnlyList<double>?> GetSparklineAsync(string canonical, CancellationToken ct);
    Task<FinanceDetail?> GetDetailAsync(string canonical, CancellationToken ct);
}
