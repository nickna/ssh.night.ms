using System.Diagnostics;
using Night.Ms.Tools.LoadTest.Bots;
using Night.Ms.Tools.LoadTest.Metrics;
using Night.Ms.Tools.LoadTest.Scenarios;

namespace Night.Ms.Tools.LoadTest.Driver;

// Ramps N bots over a window, lets them run their assigned scenarios for the configured
// duration, then signals cancellation and waits for clean disconnect with a hard ceiling.
public sealed class Driver
{
    // Cap on per-category exception messages we surface to stderr. Without it, a
    // misconfiguration at N=500 would flood the console with 500 identical stack traces.
    private const int MaxLoggedFailuresPerCategory = 3;

    private readonly DriverConfig _config;
    private readonly Func<int, IScenario> _scenarioFor;
    private readonly BotKeyStore _keyStore;
    private readonly MetricsCollector _metrics = new();
    private int _connectFailuresLogged;
    private int _scenarioFailuresLogged;

    public Driver(DriverConfig config, BotKeyStore keyStore, Func<int, IScenario> scenarioFor)
    {
        _config = config;
        _keyStore = keyStore;
        _scenarioFor = scenarioFor;
    }

    public MetricsCollector Metrics => _metrics;

    public async Task RunAsync(CancellationToken outerCt)
    {
        var totalDuration = TimeSpan.FromSeconds(_config.RampSeconds + _config.DurationSeconds);
        using var hardCts = CancellationTokenSource.CreateLinkedTokenSource(outerCt);
        hardCts.CancelAfter(totalDuration);

        // Spacing between successive bot starts. At N=500 / ramp=30s that's 60ms per bot —
        // enough that the server isn't seeing 500 simultaneous TCP SYNs at t=0 while still
        // hitting steady state inside the first half of the ramp window.
        var spacingMs = _config.RampSeconds * 1000.0 / Math.Max(1, _config.BotCount);

        var sw = Stopwatch.StartNew();
        Console.Out.WriteLine($"loadtest: ramping {_config.BotCount} bots over {_config.RampSeconds}s, then steady for {_config.DurationSeconds}s...");

        var botTasks = new Task[_config.BotCount];
        for (var i = 0; i < _config.BotCount; i++)
        {
            var index = i + 1;  // 1-based to match handle numbering loadbot-0001..
            var delay = TimeSpan.FromMilliseconds(spacingMs * i);
            botTasks[i] = RunBotAsync(index, delay, hardCts.Token);
        }

        // Wait for ramp + duration to elapse. We don't await botTasks individually — the
        // cancellation token signals each bot to stop on its own and exit RunBotAsync.
        try
        {
            await Task.Delay(totalDuration, outerCt).ConfigureAwait(false);
        }
        catch (OperationCanceledException) { /* user ctrl-c */ }

        Console.Out.WriteLine("loadtest: draining bots...");
        hardCts.Cancel();
        // Give bots up to 30s to disconnect cleanly. Stragglers are noted but don't block
        // the report from being written.
        var drain = await Task.WhenAny(Task.WhenAll(botTasks), Task.Delay(TimeSpan.FromSeconds(30))).ConfigureAwait(false);
        if (drain != Task.WhenAll(botTasks))
        {
            var stuck = botTasks.Count(t => !t.IsCompleted);
            Console.Out.WriteLine($"loadtest: {stuck} bot(s) did not disconnect within 30s grace window.");
        }
        sw.Stop();
        Console.Out.WriteLine($"loadtest: run finished in {sw.Elapsed.TotalSeconds:F1}s.");
    }

    private async Task RunBotAsync(int index, TimeSpan startDelay, CancellationToken ct)
    {
        try
        {
            await Task.Delay(startDelay, ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException) { return; }

        var handle = _keyStore.HandleFor(index);
        var keyPath = _keyStore.PathFor(index);
        await using var bot = new Bot(_config.Host, _config.Port, handle, keyPath);
        var scenario = _scenarioFor(index);

        var connectSw = Stopwatch.StartNew();
        try
        {
            await bot.ConnectAsync(ct).ConfigureAwait(false);
            connectSw.Stop();
            _metrics.Record("bot.connect_ms", connectSw.Elapsed);
        }
        catch (OperationCanceledException) { return; }
        catch (Exception ex)
        {
            _metrics.IncrementError("bot.connect");
            var logRank = Interlocked.Increment(ref _connectFailuresLogged);
            if (logRank <= MaxLoggedFailuresPerCategory)
            {
                var inner = ex.InnerException is { } i ? $" -> {i.GetType().Name}: {i.Message}" : "";
                Console.Error.WriteLine($"loadtest: bot {handle} connect failed: {ex.GetType().Name}: {ex.Message}{inner}");
            }
            // First failure only: dump the full stack so we can see *where* inside
            // Renci.SshNet the read-of-zero happened (KEX, banner exchange, auth, etc.).
            if (logRank == 1)
            {
                Console.Error.WriteLine($"loadtest: first-failure stack:\n{ex}");
                Console.Error.WriteLine(
                    $"loadtest: to bisect manually, try: ssh -p {_config.Port} -i \"{keyPath}\" -o StrictHostKeyChecking=accept-new {handle}@{_config.Host}");
            }
            return;
        }

        try
        {
            await scenario.RunAsync(bot, _metrics, ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException) { /* expected on shutdown */ }
        catch (Exception ex)
        {
            _metrics.IncrementError($"{scenario.Name}.unhandled");
            if (Interlocked.Increment(ref _scenarioFailuresLogged) <= MaxLoggedFailuresPerCategory)
            {
                Console.Error.WriteLine($"loadtest: bot {handle} scenario '{scenario.Name}' threw: {ex.GetType().Name}: {ex.Message}");
            }
        }
    }
}

public sealed record DriverConfig(
    string Host,
    int Port,
    int BotCount,
    int RampSeconds,
    int DurationSeconds);
