using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Hosting;

public sealed class SshHost(
    NightMsOptions options,
    ILoggerFactory loggerFactory,
    ILogger<SshHost> logger,
    AuthLookupService authLookup,
    IServiceProvider services) : BackgroundService
{
    private BbsSshServer? _server;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var port = ResolveListenerPort();
        _server = new BbsSshServer(
            new BbsSshServerOptions
            {
                Port = port,
                HostKeyDirectory = options.HostKeyDirectory,
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
        // BBS_SSH_PORT env var (or appsettings key) overrides the default. run.ps1 sets
        // it from its -SshPort parameter; otherwise we fall back to 2222.
        if (options.SshPort is { } p && p is > 0 and <= 65535) return p;
        return 2222;
    }

    private async Task HandleSessionAsync(BbsSession session, CancellationToken cancellationToken)
    {
        var adapter = new SshSessionAdapter(session);
        try
        {
            await BbsSessionRunner.RunAsync(services, adapter, logger, cancellationToken);
        }
        finally
        {
            try { await session.CloseAsync(cancellationToken: cancellationToken); } catch { /* already closed */ }
            logger.LogInformation("Session closed for fingerprint={Fingerprint}", session.Fingerprint);
        }
    }
}
