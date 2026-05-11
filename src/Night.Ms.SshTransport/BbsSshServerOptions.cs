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
}
