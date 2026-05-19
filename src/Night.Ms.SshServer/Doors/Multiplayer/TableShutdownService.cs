using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Night.Ms.SshServer.Doors.Multiplayer;

// IHostedService that fires registry.StopAllAsync on ApplicationStopping so seated humans
// get their chips cashed out into WinningsBalance before the process exits. Coordinator's
// own StopAsync already iterates seated humans and calls StandUpAsync (which routes
// through IMultiplayerGameLedger.CashOutAsync); we just need the trigger.
//
// If the process crashes hard (no graceful stop), TableReconciliationService handles
// recovery on next boot by walking the Redis seat hashes.
public sealed class TableShutdownService(
    ITableRegistry registry,
    ILogger<TableShutdownService> log) : IHostedService
{
    public Task StartAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        log.LogInformation("table shutdown: cashing out seated players");
        try
        {
            await registry.StopAllAsync(cancellationToken);
        }
        catch (Exception ex)
        {
            log.LogError(ex, "table shutdown failed");
        }
    }
}
