using System.Collections.Concurrent;

namespace Night.Ms.Tools.LoadTest.Metrics;

// Thread-safe collector for all per-action timings + error counters across the run.
// Scenarios call Record(name, elapsed) on success and IncrementError(name) on failure;
// Report walks both maps at the end to print/serialize.
public sealed class MetricsCollector
{
    private readonly ConcurrentDictionary<string, LatencyHistogram> _histograms = new();
    private readonly ConcurrentDictionary<string, long> _errors = new();

    public void Record(string metric, TimeSpan elapsed)
    {
        var hist = _histograms.GetOrAdd(metric, _ => new LatencyHistogram());
        hist.Record(elapsed);
    }

    public void IncrementError(string metric)
    {
        _errors.AddOrUpdate(metric, 1, (_, n) => n + 1);
    }

    public IReadOnlyDictionary<string, LatencySummary> SummarizeLatencies()
    {
        var result = new Dictionary<string, LatencySummary>(StringComparer.Ordinal);
        foreach (var (key, hist) in _histograms)
        {
            result[key] = hist.Summarize();
        }
        return result;
    }

    public IReadOnlyDictionary<string, long> SummarizeErrors()
    {
        return _errors.ToDictionary(kv => kv.Key, kv => kv.Value, StringComparer.Ordinal);
    }
}
