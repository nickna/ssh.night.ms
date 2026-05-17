using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Providers.Finance;

// Routes IFinanceProvider calls into the per-source providers by Kind. Each per-source call
// is wrapped so an upstream failure surfaces as null — the screen renders "—" rather than
// crashing if Yahoo blocks or CoinGecko times out.
internal sealed class CompositeFinanceProvider(
    IStockQuoteProvider stocks,
    ICryptoQuoteProvider crypto,
    IFxQuoteProvider fx,
    ILogger<CompositeFinanceProvider> logger) : IFinanceProvider
{
    public Task<FinanceQuote?> GetQuoteAsync(WatchlistKind kind, string canonical, CancellationToken ct = default) =>
        Safe(kind switch
        {
            WatchlistKind.Stock => stocks.GetQuoteAsync(canonical, ct),
            WatchlistKind.Crypto => crypto.GetQuoteAsync(canonical, ct),
            WatchlistKind.Fx => fx.GetQuoteAsync(canonical, ct),
            _ => throw new ArgumentOutOfRangeException(nameof(kind), kind, null),
        }, $"quote {kind} {canonical}");

    public Task<IReadOnlyList<double>?> GetSparklineAsync(WatchlistKind kind, string canonical, CancellationToken ct = default) =>
        Safe(kind switch
        {
            WatchlistKind.Stock => stocks.GetSparklineAsync(canonical, ct),
            WatchlistKind.Crypto => crypto.GetSparklineAsync(canonical, ct),
            WatchlistKind.Fx => fx.GetSparklineAsync(canonical, ct),
            _ => throw new ArgumentOutOfRangeException(nameof(kind), kind, null),
        }, $"sparkline {kind} {canonical}");

    public Task<FinanceDetail?> GetDetailAsync(WatchlistKind kind, string canonical, CancellationToken ct = default) =>
        Safe(kind switch
        {
            WatchlistKind.Stock => stocks.GetDetailAsync(canonical, ct),
            WatchlistKind.Crypto => crypto.GetDetailAsync(canonical, ct),
            WatchlistKind.Fx => fx.GetDetailAsync(canonical, ct),
            _ => throw new ArgumentOutOfRangeException(nameof(kind), kind, null),
        }, $"detail {kind} {canonical}");

    private async Task<T?> Safe<T>(Task<T?> body, string context) where T : class
    {
        try { return await body.ConfigureAwait(false); }
        catch (OperationCanceledException) { throw; }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Finance fetch '{Context}' failed; surfacing null.", context);
            return null;
        }
    }
}

public static class CompositeFinanceProviderRegistration
{
    public static IServiceCollection AddCompositeFinanceProvider(this IServiceCollection services)
    {
        services.AddSingleton<IFinanceProvider, CompositeFinanceProvider>();
        return services;
    }
}
