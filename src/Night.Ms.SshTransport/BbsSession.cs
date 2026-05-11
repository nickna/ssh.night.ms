using System.Security.Claims;
using Microsoft.DevTunnels.Ssh;
using Microsoft.DevTunnels.Ssh.Algorithms;

namespace Night.Ms.SshTransport;

public sealed class BbsSession
{
    private readonly TaskCompletionSource _closed = new(TaskCreationOptions.RunContinuationsAsynchronously);

    internal BbsSession(SshChannel channel, ClaimsPrincipal principal, string fingerprint, string keyAlgorithm, byte[] publicKeyBlob, AuthDecision authDecision, PtyInfo? pty)
    {
        Channel = channel;
        Principal = principal;
        Fingerprint = fingerprint;
        KeyAlgorithm = keyAlgorithm;
        PublicKeyBlob = publicKeyBlob;
        AuthDecision = authDecision;
        Pty = pty;
        Stream = new SshStream(channel);
        channel.Closed += (_, _) => _closed.TrySetResult();
    }

    public SshChannel Channel { get; }
    public ClaimsPrincipal Principal { get; }
    public string Fingerprint { get; }
    public string KeyAlgorithm { get; }
    public byte[] PublicKeyBlob { get; }
    public AuthDecision AuthDecision { get; internal set; }
    public PtyInfo? Pty { get; internal set; }
    public Stream Stream { get; }

    public event EventHandler<WindowChange>? WindowChanged;
    internal void RaiseWindowChanged(WindowChange change) => WindowChanged?.Invoke(this, change);

    public Task Closed => _closed.Task;

    public Task CloseAsync(uint exitStatus = 0, CancellationToken cancellationToken = default)
        => Channel.CloseAsync(exitStatus, cancellationToken);
}
