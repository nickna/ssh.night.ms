using System.Net;
using System.Security.Claims;
using Microsoft.DevTunnels.Ssh;
using Microsoft.DevTunnels.Ssh.Algorithms;

namespace Night.Ms.SshTransport;

public sealed class BbsSession
{
    private readonly TaskCompletionSource _closed = new(TaskCreationOptions.RunContinuationsAsynchronously);

    internal BbsSession(SshChannel channel, ClaimsPrincipal principal, string fingerprint, string keyAlgorithm, byte[] publicKeyBlob, AuthDecision authDecision, PtyInfo? pty, IPAddress? remoteIPAddress, string? offeredFingerprint = null, string? offeredAlgorithm = null, byte[]? offeredBlob = null, string? offeredPassword = null)
    {
        Channel = channel;
        Principal = principal;
        Fingerprint = fingerprint;
        KeyAlgorithm = keyAlgorithm;
        PublicKeyBlob = publicKeyBlob;
        AuthDecision = authDecision;
        Pty = pty;
        RemoteIPAddress = remoteIPAddress;
        OfferedFingerprint = offeredFingerprint;
        OfferedAlgorithm = offeredAlgorithm;
        OfferedBlob = offeredBlob;
        OfferedPassword = offeredPassword;
        Stream = new SshStream(channel);
        channel.Closed += (_, _) => _closed.TrySetResult();
    }

    public SshChannel Channel { get; }
    public ClaimsPrincipal Principal { get; }
    // The credential that actually authenticated this session. For publickey auth, these
    // reflect the key. For password auth, Fingerprint is "" and PublicKeyBlob is empty.
    public string Fingerprint { get; }
    public string KeyAlgorithm { get; }
    public byte[] PublicKeyBlob { get; }
    // A key the client OFFERED during auth (publickey-query phase) that ultimately wasn't
    // used to authenticate this session — typically because the user authed by password
    // after a failed publickey attempt, or because the user is brand-new and signing up via
    // SSH with a key in their agent. The TUI uses this to prompt "adopt this key?" after
    // successful login. Null when no key was offered at all.
    public string? OfferedFingerprint { get; }
    public string? OfferedAlgorithm { get; }
    public byte[]? OfferedBlob { get; }
    // Plaintext password the client typed at the SSH "password" prompt, surfaced ONLY when
    // the handle didn't exist (SignupRequired). Lets the signup screen pre-fill the password
    // fields so the user doesn't have to re-type what they just typed. Null otherwise —
    // never set on Known-user auth (no reason to retain a successful password) and never set
    // on publickey auth (no password was typed).
    public string? OfferedPassword { get; }
    public AuthDecision AuthDecision { get; internal set; }
    public PtyInfo? Pty { get; internal set; }
    public Stream Stream { get; }
    // Peer IP as captured by IpCapturingTcpSshServer at TCP accept time. Null only for
    // unusual transports (e.g. Unix sockets) — for any inbound TCP connection this is set.
    public IPAddress? RemoteIPAddress { get; }

    public event EventHandler<WindowChange>? WindowChanged;
    internal void RaiseWindowChanged(WindowChange change) => WindowChanged?.Invoke(this, change);

    public Task Closed => _closed.Task;

    public Task CloseAsync(uint exitStatus = 0, CancellationToken cancellationToken = default)
        => Channel.CloseAsync(exitStatus, cancellationToken);
}
