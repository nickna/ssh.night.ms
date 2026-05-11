using System.Net;
using System.Net.Sockets;
using Microsoft.DevTunnels.Ssh;
using TcpSshServer = Microsoft.DevTunnels.Ssh.Tcp.SshServer;

namespace Night.Ms.SshTransport;

// DevTunnels' SshServer exposes no public surface for the accepted peer's IP — the
// AcceptConnectionAsync override hands back a NetworkStream and forgets the TcpClient.
// We subclass and replicate the accept logic so we can keep the RemoteEndPoint, then
// correlate it with the next SshServerSession via the SessionOpened event (which fires
// synchronously between the accept and ConnectAsync, so the field is safe).
internal sealed class IpCapturingTcpSshServer : TcpSshServer
{
    private readonly object _gate = new();
    private IPEndPoint? _pendingRemoteEndpoint;

    public IpCapturingTcpSshServer(SshSessionConfiguration config, System.Diagnostics.TraceSource trace)
        : base(config, trace)
    {
    }

    public IPEndPoint? ConsumePendingRemoteEndpoint()
    {
        lock (_gate)
        {
            var ep = _pendingRemoteEndpoint;
            _pendingRemoteEndpoint = null;
            return ep;
        }
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

        lock (_gate)
        {
            _pendingRemoteEndpoint = tcpClient.Client.RemoteEndPoint as IPEndPoint;
        }
        return tcpClient.GetStream();
    }
}
