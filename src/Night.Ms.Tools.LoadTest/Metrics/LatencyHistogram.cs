using System.Collections.Concurrent;

namespace Night.Ms.Tools.LoadTest.Metrics;

// Per-metric latency collector. Records elapsed times as microseconds in a ConcurrentBag,
// computes percentiles at flush time by sorting a snapshot copy. For our scale (a few
// million samples max per run), sort-on-flush is faster and simpler than a streaming
// HDR-style histogram, and avoids a dependency.
public sealed class LatencyHistogram
{
    private readonly ConcurrentBag<long> _samples = new();

    public void Record(TimeSpan elapsed)
    {
        // Microsecond resolution: ms is too coarse for fast actions, ns is overkill and
        // wastes long range. We never measure anything > ~30 minutes per call, so a
        // signed long microsecond fits trivially.
        var micros = (long)(elapsed.TotalMilliseconds * 1000.0);
        if (micros < 0) micros = 0;
        _samples.Add(micros);
    }

    public LatencySummary Summarize()
    {
        var arr = _samples.ToArray();
        if (arr.Length == 0)
        {
            return new LatencySummary(0, 0, 0, 0, 0, 0, 0);
        }
        Array.Sort(arr);
        return new LatencySummary(
            Count: arr.Length,
            MinMs: arr[0] / 1000.0,
            P50Ms: arr[Percentile(arr.Length, 0.50)] / 1000.0,
            P95Ms: arr[Percentile(arr.Length, 0.95)] / 1000.0,
            P99Ms: arr[Percentile(arr.Length, 0.99)] / 1000.0,
            MaxMs: arr[^1] / 1000.0,
            MeanMs: arr.Average() / 1000.0);
    }

    private static int Percentile(int n, double p)
    {
        // Nearest-rank percentile (P95 of n=100 → index 94). Good enough for our purposes;
        // exact interpolation isn't useful when samples are inherently noisy.
        var idx = (int)Math.Ceiling(p * n) - 1;
        if (idx < 0) return 0;
        if (idx >= n) return n - 1;
        return idx;
    }
}

public sealed record LatencySummary(
    long Count,
    double MinMs,
    double P50Ms,
    double P95Ms,
    double P99Ms,
    double MaxMs,
    double MeanMs);
