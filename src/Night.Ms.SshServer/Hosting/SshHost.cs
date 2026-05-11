using System.Text;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Hosting;

public sealed class SshHost(IConfiguration configuration, ILoggerFactory loggerFactory, ILogger<SshHost> logger) : BackgroundService
{
    private BbsSshServer? _server;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var port = ResolveListenerPort();
        _server = new BbsSshServer(
            new BbsSshServerOptions { Port = port },
            loggerFactory.CreateLogger<BbsSshServer>());

        _server.SessionStarted += HandleSessionAsync;

        await _server.StartAsync(stoppingToken);
        logger.LogInformation("ssh.night.ms listening on :{Port} (Microsoft.DevTunnels.Ssh; M2 placeholder shell)", port);

        // BackgroundService keeps us alive until shutdown.
        try
        {
            await Task.Delay(Timeout.Infinite, stoppingToken);
        }
        catch (OperationCanceledException)
        {
            // expected on shutdown
        }
    }

    public override async Task StopAsync(CancellationToken cancellationToken)
    {
        if (_server is not null)
        {
            await _server.DisposeAsync();
        }
        await base.StopAsync(cancellationToken);
    }

    private int ResolveListenerPort()
    {
        // Aspire injects SSH endpoint info via SERVICES__SSH__SSH-LISTENER__0; fall back to 2222.
        foreach (var key in new[] { "services:ssh:ssh-listener:0", "Services:ssh:ssh-listener:0" })
        {
            var value = configuration[key];
            if (!string.IsNullOrWhiteSpace(value) && Uri.TryCreate(value, UriKind.Absolute, out var uri) && uri.Port > 0)
            {
                return uri.Port;
            }
        }
        return 2222;
    }

    private async Task HandleSessionAsync(BbsSession session, CancellationToken cancellationToken)
    {
        // M2 placeholder: prove the channel works end-to-end by sending a banner and echoing input
        // until the client closes. M4 replaces this with the Terminal.Gui driver.
        var banner = new StringBuilder()
            .Append("\r\n")
            .Append("[1;36mssh.night.ms[0m [2mM2 transport check[0m\r\n")
            .Append("\r\n")
            .Append($"  fingerprint  {session.Fingerprint}\r\n")
            .Append($"  algorithm    {session.KeyAlgorithm}\r\n")
            .Append($"  pty          {session.Pty?.Terminal ?? "<none>"} {session.Pty?.Cols}x{session.Pty?.Rows}\r\n")
            .Append("\r\n")
            .Append("Type to echo. Send EOF (Ctrl+D) or close to disconnect.\r\n\r\n")
            .ToString();

        var bannerBytes = Encoding.UTF8.GetBytes(banner);
        await session.Stream.WriteAsync(bannerBytes, cancellationToken);
        await session.Stream.FlushAsync(cancellationToken);

        var buffer = new byte[4096];
        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                var read = await session.Stream.ReadAsync(buffer, cancellationToken);
                if (read == 0) break;
                await session.Stream.WriteAsync(buffer.AsMemory(0, read), cancellationToken);
                await session.Stream.FlushAsync(cancellationToken);
            }
        }
        catch (Exception ex) when (ex is IOException or ObjectDisposedException)
        {
            // client disconnected
        }
        finally
        {
            await session.CloseAsync(cancellationToken: cancellationToken);
            logger.LogInformation("Session closed for fingerprint={Fingerprint}", session.Fingerprint);
        }
    }
}
