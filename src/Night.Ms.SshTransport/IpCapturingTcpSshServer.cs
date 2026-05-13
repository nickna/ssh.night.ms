using System.Net;
using System.Net.Sockets;
using Microsoft.DevTunnels.Ssh;
using Microsoft.Extensions.Logging;
using TcpSshServer = Microsoft.DevTunnels.Ssh.Tcp.SshServer;

namespace Night.Ms.SshTransport;

// DevTunnels' SshServer exposes no public surface for the accepted peer's IP — the
// AcceptConnectionAsync override hands back a NetworkStream and forgets the TcpClient,
// and SshSession doesn't surface the underlying transport stream or RemoteEndPoint.
//
// We subclass and replicate the accept logic so we can keep the RemoteEndPoint, then
// correlate it with the next SshServerSession via the SessionOpened event. This works
// because DevTunnels' AcceptSessionsAsync loop is strictly serial:
//
//   while (true) {
//     stream = await AcceptConnectionAsync(...);   // our override sets the slot
//     SessionOpened?.Invoke(this, session);        // synchronous — consumes the slot
//     _ = Task.Run(() => session.ConnectAsync(stream, ...));   // background; loop continues
//   }
//
// SessionOpened runs on the accept-loop thread before the next AcceptConnectionAsync is
// called, so the single-slot field is safe. We log if we ever observe the assumption
// breaking (a non-null slot at accept time, or a null slot at consume time) so a future
// DevTunnels behavior change is loud rather than silent IP misattribution.
internal sealed class IpCapturingTcpSshServer : TcpSshServer
{
    private readonly object _gate = new();
    private readonly ILogger _logger;
    private IPEndPoint? _pendingRemoteEndpoint;

    public IpCapturingTcpSshServer(SshSessionConfiguration config, System.Diagnostics.TraceSource trace, ILogger logger)
        : base(config, trace)
    {
        _logger = logger;
    }

    public IPEndPoint? ConsumePendingRemoteEndpoint()
    {
        IPEndPoint? ep;
        lock (_gate)
        {
            ep = _pendingRemoteEndpoint;
            _pendingRemoteEndpoint = null;
        }
        if (ep is null)
        {
            _logger.LogDebug("No pending remote endpoint at SessionOpened — IP attribution will fall back to <unknown>.");
        }
        return ep;
    }

    protected override async Task<Stream?> AcceptConnectionAsync(TcpListener listener)
    {
        TcpClient tcpClient;
        try
        {
            tcpClient = await listener.AcceptTcpClientAsync().ConfigureAwait(false);
        }
        catch (SocketException) { return null; }
        catch (ObjectDisposedException) { return null; }

        // Match the upstream tuning ConfigureSocketOptionsForSsh applies: disable Nagle,
        // long-lived SSH sessions benefit from immediate small-write delivery.
        try { tcpClient.NoDelay = true; } catch { /* best-effort */ }

        var endpoint = tcpClient.Client.RemoteEndPoint as IPEndPoint;
        IPEndPoint? stale;
        lock (_gate)
        {
            stale = _pendingRemoteEndpoint;
            _pendingRemoteEndpoint = endpoint;
        }
        if (stale is not null)
        {
            // The previous iteration's SessionOpened never consumed the slot — the documented
            // serial accept→SessionOpened contract has been broken. Subsequent IP attribution
            // is unreliable until the next clean accept.
            _logger.LogWarning(
                "Stale pending remote endpoint {StaleEndpoint} overwritten by {NewEndpoint} at accept — DevTunnels accept-loop ordering may have changed; IP attribution is unsafe.",
                stale,
                endpoint);
        }
        return tcpClient.GetStream();
    }
}
