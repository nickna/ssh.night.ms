using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Providers.Finance;

// Inferred shape of a user-typed symbol: which provider routes it and what canonical key
// the provider expects. DisplayHint is short-form text shown in the add-prompt preview
// before the network call resolves a real name.
public sealed record ResolvedSymbol(
    WatchlistKind Kind,
    string Canonical,
    string DisplayHint);

// Maps raw user input ("BTC", "btc", "c:bitcoin", "EUR/USD") to (WatchlistKind, Canonical).
//
// Auto-detection rules:
//   1. Empty / whitespace → null.
//   2. Type-prefix override: "s:<sym>" / "c:<sym>" / "fx:<sym>" forces the kind. This is the
//      escape hatch for ambiguous tickers (e.g. forcing crypto on a 3-letter symbol that
//      happens to also be a stock).
//   3. Contains '/' or ':' → FX. Both halves uppercased and concatenated (EUR/USD → EURUSD).
//   4. Matches the embedded crypto symbol → CoinGecko id map → Crypto.
//   5. Otherwise → Stock, canonical = upper-cased raw.
//
// The embedded crypto map covers the top ~20 by market cap. Unknown crypto tickers route to
// Stock; users force routing with the c: prefix and CoinGeckoProvider does a /coins/list
// lookup to translate the symbol when the embedded map misses.
public static class SymbolResolver
{
    // Embedded BTC → CoinGecko id map for the most common cryptos. Keys are upper-cased
    // ticker symbols. Values are the CoinGecko canonical ids exactly as the API expects.
    private static readonly IReadOnlyDictionary<string, string> CryptoTickerToCoinGeckoId =
        new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            ["BTC"] = "bitcoin",
            ["ETH"] = "ethereum",
            ["SOL"] = "solana",
            ["ADA"] = "cardano",
            ["DOGE"] = "dogecoin",
            ["AVAX"] = "avalanche-2",
            ["DOT"] = "polkadot",
            ["MATIC"] = "matic-network",
            ["LINK"] = "chainlink",
            ["XRP"] = "ripple",
            ["BNB"] = "binancecoin",
            ["ATOM"] = "cosmos",
            ["LTC"] = "litecoin",
            ["TRX"] = "tron",
            ["SHIB"] = "shiba-inu",
            ["UNI"] = "uniswap",
            ["XLM"] = "stellar",
            ["NEAR"] = "near",
            ["APT"] = "aptos",
            ["ICP"] = "internet-computer",
        };

    public static ResolvedSymbol? Resolve(string? raw)
    {
        if (string.IsNullOrWhiteSpace(raw)) return null;
        var trimmed = raw.Trim();

        // Type-prefix override. Strip the prefix, then run the matching kind's normalization.
        if (TryStripPrefix(trimmed, "s:", out var stockRest)) return ResolveAsStock(stockRest);
        if (TryStripPrefix(trimmed, "c:", out var cryptoRest)) return ResolveAsCrypto(cryptoRest);
        if (TryStripPrefix(trimmed, "fx:", out var fxRest)) return ResolveAsFx(fxRest);

        if (LooksLikeFxPair(trimmed)) return ResolveAsFx(trimmed);

        var upper = trimmed.ToUpperInvariant();
        if (CryptoTickerToCoinGeckoId.TryGetValue(upper, out var coinId))
            return new ResolvedSymbol(WatchlistKind.Crypto, coinId, $"{upper} (crypto)");

        return ResolveAsStock(trimmed);
    }

    private static bool TryStripPrefix(string s, string prefix, out string rest)
    {
        if (s.StartsWith(prefix, StringComparison.OrdinalIgnoreCase) && s.Length > prefix.Length)
        {
            rest = s[prefix.Length..].Trim();
            return rest.Length > 0;
        }
        rest = string.Empty;
        return false;
    }

    private static bool LooksLikeFxPair(string s) => s.Contains('/') || s.Contains(':');

    private static ResolvedSymbol? ResolveAsStock(string raw)
    {
        var clean = raw.Trim();
        if (clean.Length == 0) return null;
        // Permissive: lots of exchanges allow ".", "-", or "^" (e.g. BRK.B, BTC-USD on Yahoo,
        // ^GSPC). Upper-case only — Yahoo's symbol space is case-insensitive on the wire.
        var canonical = clean.ToUpperInvariant();
        return new ResolvedSymbol(WatchlistKind.Stock, canonical, $"{canonical} (stock)");
    }

    private static ResolvedSymbol? ResolveAsCrypto(string raw)
    {
        var clean = raw.Trim();
        if (clean.Length == 0) return null;
        var upper = clean.ToUpperInvariant();
        // Prefer the embedded ticker map; otherwise assume the user typed a CoinGecko id
        // ("dogecoin") or close to one. CoinGeckoProvider applies the same normalization
        // before dispatching to /coins/list as a fallback lookup.
        if (CryptoTickerToCoinGeckoId.TryGetValue(upper, out var coinId))
            return new ResolvedSymbol(WatchlistKind.Crypto, coinId, $"{upper} (crypto)");
        var canonical = clean.ToLowerInvariant();
        return new ResolvedSymbol(WatchlistKind.Crypto, canonical, $"{canonical} (crypto)");
    }

    private static ResolvedSymbol? ResolveAsFx(string raw)
    {
        var pair = SplitFxPair(raw);
        if (pair is null) return null;
        var (b, q) = pair.Value;
        var canonical = $"{b}{q}";
        return new ResolvedSymbol(WatchlistKind.Fx, canonical, $"{b}/{q} (fx)");
    }

    private static (string Base, string Quote)? SplitFxPair(string raw)
    {
        var s = raw.Trim().ToUpperInvariant();
        // Accept "EUR/USD", "EUR:USD", "EUR-USD", or "EURUSD" (concatenated 6 chars).
        char[] separators = ['/', ':', '-'];
        foreach (var sep in separators)
        {
            var idx = s.IndexOf(sep);
            if (idx > 0 && idx < s.Length - 1)
            {
                var b = s[..idx].Trim();
                var q = s[(idx + 1)..].Trim();
                if (IsCurrencyCode(b) && IsCurrencyCode(q)) return (b, q);
                return null;
            }
        }
        if (s.Length == 6 && IsCurrencyCode(s[..3]) && IsCurrencyCode(s[3..]))
            return (s[..3], s[3..]);
        return null;
    }

    private static bool IsCurrencyCode(string s) =>
        s.Length == 3 && s.All(c => c is >= 'A' and <= 'Z');
}
