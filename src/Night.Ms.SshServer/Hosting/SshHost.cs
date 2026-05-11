using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Hosting;

public sealed class SshHost(
    IConfiguration configuration,
    ILoggerFactory loggerFactory,
    ILogger<SshHost> logger,
    AuthLookupService authLookup,
    IServiceProvider services) : BackgroundService
{
    private BbsSshServer? _server;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var port = ResolveListenerPort();
        var hostKeyDir = configuration["NIGHTMS_HOST_KEY_DIR"] ?? configuration["HostKeyDirectory"];
        _server = new BbsSshServer(
            new BbsSshServerOptions
            {
                Port = port,
                HostKeyDirectory = hostKeyDir,
                AuthLookup = authLookup.LookupAsync,
            },
            loggerFactory.CreateLogger<BbsSshServer>());

        _server.SessionStarted += HandleSessionAsync;

        await _server.StartAsync(stoppingToken);
        logger.LogInformation("ssh.night.ms listening on :{Port} (Microsoft.DevTunnels.Ssh + Terminal.Gui driver)", port);

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
        try
        {
            await BbsSessionRunner.RunAsync(services, session, logger, cancellationToken);
        }
        finally
        {
            try { await session.CloseAsync(cancellationToken: cancellationToken); } catch { /* already closed */ }
            logger.LogInformation("Session closed for fingerprint={Fingerprint}", session.Fingerprint);
        }
    }
}
