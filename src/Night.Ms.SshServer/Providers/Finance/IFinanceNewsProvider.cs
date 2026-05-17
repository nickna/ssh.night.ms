namespace Night.Ms.SshServer.Providers.Finance;

// Finance-specific news, optionally filtered to a list of tickers. Returns NewsHeadline so
// callers can hand items to the existing ReaderScreen without a second type.
//
// Implementations cache; the screen calls this on every open + manual refresh.
public interface IFinanceNewsProvider
{
    Task<IReadOnlyList<NewsHeadline>> GetForTickersAsync(IReadOnlyList<string> tickers, int max, CancellationToken ct = default);
}
