namespace Night.Ms.SshTransport;

public sealed class BbsSshServerOptions
{
    public required int Port { get; init; }
    public string ListenAddress { get; init; } = "0.0.0.0";

    /// <summary>
    /// Directory where persistent host keys (ssh_host_rsa_key, ssh_host_ecdsa_key) live. If
    /// <see langword="null"/>, fresh keys are generated each startup and clients see
    /// host-key-changed warnings between restarts.
    /// </summary>
    public string? HostKeyDirectory { get; init; }

    /// <summary>
    /// Looks up the auth decision for an incoming key. Called from the SSH library's auth pipeline.
    /// Banned → connection rejected at the protocol level. Known/Unknown → connection proceeds and
    /// the decision is attached to <see cref="BbsSession.AuthDecision"/> for the TUI to dispatch on.
    /// </summary>
    public required Func<AuthQuery, CancellationToken, Task<AuthDecision>> AuthLookup { get; init; }

    /// <summary>
    /// Register curve25519-sha256 as a key exchange option. <b>OFF by default</b> because
    /// DevTunnels' <c>KeyExchangeService.ComputeExchangeHash</c> wraps <c>Q_C</c>/<c>Q_S</c>
    /// as bigints, which breaks RFC 8731 for X25519 keys with the high bit set. Clients fall
    /// back to <c>ecdh-sha2-nistp256</c> cleanly. The Curve25519 math + tests are still in
    /// the assembly so the implementation stays validated; only the algorithm registration is
    /// gated. Do not flip this on without an upstream DevTunnels fix.
    /// </summary>
    public bool EnableCurve25519KeyExchange { get; init; }
}
