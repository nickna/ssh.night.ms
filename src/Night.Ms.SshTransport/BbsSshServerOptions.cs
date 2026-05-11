namespace Night.Ms.SshTransport;

public sealed class BbsSshServerOptions
{
    public required int Port { get; init; }
    public string ListenAddress { get; init; } = "0.0.0.0";
}
