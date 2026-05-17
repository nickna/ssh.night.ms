using System.Net;

namespace Night.Ms.SshTransport;

// Discriminated query: the host's auth callback dispatches on the concrete subtype rather
// than reading nullable fields. Replaces the single-shape AuthQuery that only carried
// publickey credentials.
//   PublicKeyQuery: SSH "publickey" auth phase 1 — the client is asking "would you accept
//                   this key?" before signing. Return Known to advance, anything else to
//                   reject the offer and let the client try the next method.
//   PublicKey:      SSH "publickey" auth phase 2 — the client has signed; verify the
//                   fingerprint resolves to the user.
//   Password:       SSH "password" auth — the user typed a password. Source IP is included
//                   so the host can rate-limit per IP as well as per handle.
//   None:           SSH "none" auth — used as a method-discovery probe and as the signup
//                   on-ramp for unknown usernames. The host typically returns
//                   SignupRequired here when the handle doesn't exist.
public abstract record AuthQuery
{
    public sealed record PublicKeyQuery(string Handle, string Fingerprint, string Algorithm, byte[] Blob, IPAddress? SourceIp) : AuthQuery;
    public sealed record PublicKey(string Handle, string Fingerprint, string Algorithm, byte[] Blob, IPAddress? SourceIp) : AuthQuery;
    public sealed record Password(string Handle, string Secret, IPAddress? SourceIp) : AuthQuery;
    public sealed record None(string Handle, IPAddress? SourceIp) : AuthQuery;
}

// Outcome of an auth callback. The transport translates these into protocol-level outcomes:
//   Known           → ClaimsPrincipal returned, client lands on the BBS as the named user.
//                     OfferedFingerprint/Algorithm/Blob carry the SSH key the client
//                     offered (if any) even when auth succeeded via password — so the TUI
//                     can prompt the user to adopt the key.
//   SignupRequired  → ClaimsPrincipal returned with NameIdentifier='-1'; client lands on
//                     the signup screen with Handle prefilled. Offered key is preserved
//                     for optional adoption during signup.
//   Banned          → null returned, connection refused at SSH layer with the reason
//                     logged. Failure message is best-effort surfaced to the client.
//   RateLimited     → null returned, connection refused. RetryAfter is logged but not
//                     deliberately exposed to the client (don't help bots time their
//                     retries).
public abstract record AuthDecision
{
    public sealed record Known(
        long UserId,
        string Handle,
        bool IsSysop,
        string? OfferedFingerprint = null,
        string? OfferedAlgorithm = null,
        byte[]? OfferedBlob = null) : AuthDecision;

    public sealed record SignupRequired(
        string Handle,
        string? OfferedFingerprint = null,
        string? OfferedAlgorithm = null,
        byte[]? OfferedBlob = null) : AuthDecision;

    public sealed record Banned(string Reason) : AuthDecision;

    public sealed record RateLimited(TimeSpan RetryAfter) : AuthDecision;

    // "This specific credential didn't authenticate; advertise other methods and let the
    // client try again." Used when a known handle presents an unregistered key (client falls
    // through to password) or types a wrong password without hitting the lockout threshold.
    // Distinguished from Banned/RateLimited so logs aren't misleading.
    public sealed record Refused(string Reason) : AuthDecision;
}
