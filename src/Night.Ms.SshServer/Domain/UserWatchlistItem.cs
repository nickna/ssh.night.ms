namespace Night.Ms.SshServer.Domain;

// Discriminator for the three data sources behind IFinanceProvider. Stored as int in the DB
// (default EF Core enum convention) so a future addition (futures, indices, options) appends
// without renumbering.
public enum WatchlistKind { Stock = 0, Crypto = 1, Fx = 2 }

// A row on the user's finance dashboard watchlist. One row per (UserId, Canonical) — the raw
// Symbol is whatever the user typed ("BTC", "btc", "c:bitcoin"), while Canonical is what
// SymbolResolver normalized it to ("bitcoin") and is what we actually feed the provider. That
// split keeps the display column verbatim while preventing duplicate watchlist entries that
// resolve to the same underlying instrument.
//
// SortOrder drives display order on FinanceScreen. There's no DB constraint on uniqueness of
// SortOrder per user — gaps from deletes are harmless because OrderBy(SortOrder).ThenBy(Id)
// keeps ordering deterministic.
public sealed class UserWatchlistItem
{
    public long Id { get; set; }
    public long UserId { get; set; }
    public User? User { get; set; }

    // The verbatim text the user typed. Preserved for display so a user who entered "btc" sees
    // "btc" rather than the resolver's normalization. Capped at 32 to fit comfortably in the
    // SYMBOL column on an 80-wide PTY.
    public required string Symbol { get; set; }

    // SymbolResolver's normalized lookup key. Stocks: "AAPL". Crypto: CoinGecko id like
    // "bitcoin". FX: "USDEUR" (base + quote concatenated). What IFinanceProvider receives.
    public required string Canonical { get; set; }

    public WatchlistKind Kind { get; set; }
    public int SortOrder { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
}
