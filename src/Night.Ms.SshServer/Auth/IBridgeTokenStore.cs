namespace Night.Ms.SshServer.Auth;

// Single-use, short-lived tokens that bridge an authenticated SSH session into a web cookie
// session. The TUI mints one via IssueAsync, displays the URL to the user, and the GET
// /auth/bridge/{token} endpoint redeems it. Tokens are URL-safe base64 of 32 random bytes,
// stored hashed at rest so a Redis dump doesn't yield working credentials.
public interface IBridgeTokenStore
{
    // Returns the raw token to embed in the URL, or null if the user has already issued the
    // per-hour maximum (5). Per-user rate-limit lives in the store so callers don't each
    // have to reimplement it.
    Task<string?> IssueAsync(long userId, CancellationToken ct);

    // Returns the userId the token was issued for, or null if missing/expired/already used.
    // Redemption is atomic (single Redis GETDEL) so a token cannot be redeemed twice even
    // under concurrent clicks.
    Task<long?> RedeemAsync(string token, CancellationToken ct);
}
