using Night.Ms.Tools.LoadTest.Bots;
using Night.Ms.Tools.LoadTest.Metrics;

namespace Night.Ms.Tools.LoadTest.Scenarios;

// Sits on the lobby and renders ambient updates (presence, weather refresh) but issues
// no input. Measures the steady-state cost of an open session that's just *present* —
// useful for distinguishing per-session overhead from per-action overhead in the report.
public sealed class IdleScenario : IScenario
{
    public string Name => "idle";

    public async Task RunAsync(Bot bot, MetricsCollector metrics, CancellationToken ct)
    {
        // Lobby paints "Welcome back, {handle}." as a label — uniqueness comes from the
        // word "Welcome", which neither the carousel labels nor the art include.
        var sw = System.Diagnostics.Stopwatch.StartNew();
        var landed = await bot.Screen.WaitForAsync("Welcome", TimeSpan.FromSeconds(20), ct).ConfigureAwait(false);
        sw.Stop();
        if (!landed)
        {
            metrics.IncrementError("lobby.land");
            return;
        }
        metrics.Record("lobby.land_ms", sw.Elapsed);

        try
        {
            await Task.Delay(Timeout.InfiniteTimeSpan, ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException) { /* expected */ }
    }
}
