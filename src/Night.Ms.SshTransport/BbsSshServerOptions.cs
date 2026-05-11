namespace Night.Ms.SshTransport;

public sealed class BbsSshServerOptions
{
    public required int Port { get; init; }
    public string ListenAddress { get; init; } = "0.0.0.0";

    /// <summary>
    /// Looks up the auth decision for an incoming key. Called from the SSH library's auth pipeline.
    /// Banned → connection rejected at the protocol level. Known/Unknown → connection proceeds and
    /// the decision is attached to <see cref="BbsSession.AuthDecision"/> for the TUI to dispatch on.
    /// </summary>
    public required Func<AuthQuery, CancellationToken, Task<AuthDecision>> AuthLookup { get; init; }
}
