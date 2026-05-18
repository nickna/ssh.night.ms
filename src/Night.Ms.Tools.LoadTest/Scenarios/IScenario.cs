using Night.Ms.Tools.LoadTest.Bots;
using Night.Ms.Tools.LoadTest.Metrics;

namespace Night.Ms.Tools.LoadTest.Scenarios;

// A scenario takes a freshly-connected bot (already sitting at the lobby) and drives it
// until the cancellation token fires. Implementations should report timing and error
// counters through the supplied MetricsCollector — they don't own any output of their
// own. Errors that should fail the run get rethrown; transient ones that just count
// against an error budget should be swallowed after metrics.IncrementError(...).
public interface IScenario
{
    string Name { get; }
    Task RunAsync(Bot bot, MetricsCollector metrics, CancellationToken ct);
}
