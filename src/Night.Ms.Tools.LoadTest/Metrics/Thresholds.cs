using System.Text.Json;

namespace Night.Ms.Tools.LoadTest.Metrics;

// Gate config loaded from a JSON file. Per-metric thresholds for the
// percentiles we care about and a budget for the error rate. Any
// threshold can be null/missing and is then skipped.
//
// Example:
//   {
//     "chat.publish_to_receive_ms": { "p95_ms": 500, "p99_ms": 1500, "max_error_rate": 0.001 },
//     "forum.new_topic_ms":         { "p95_ms": 1000, "max_error_rate": 0.001 }
//   }
public sealed class Thresholds
{
    private readonly IReadOnlyDictionary<string, MetricThreshold> _byMetric;

    public Thresholds(IReadOnlyDictionary<string, MetricThreshold> byMetric)
    {
        _byMetric = byMetric;
    }

    public static Thresholds Load(string path)
    {
        var json = File.ReadAllText(path);
        var doc = JsonSerializer.Deserialize<Dictionary<string, MetricThreshold>>(
            json,
            new JsonSerializerOptions { PropertyNameCaseInsensitive = true })
            ?? throw new InvalidOperationException("threshold file is empty or invalid");
        return new Thresholds(doc);
    }

    public GateResult Evaluate(MetricsCollector metrics)
    {
        var failures = new List<string>();
        var latencies = metrics.SummarizeLatencies();
        var errors = metrics.SummarizeErrors();

        foreach (var (metric, thresh) in _byMetric)
        {
            latencies.TryGetValue(metric, out var summary);
            errors.TryGetValue(metric, out var errCount);
            var total = (summary?.Count ?? 0) + errCount;

            if (thresh.P50Ms is { } p50 && summary is { P50Ms: var actual } && actual > p50)
                failures.Add($"{metric}: p50 {actual:F1}ms exceeds threshold {p50:F1}ms");
            if (thresh.P95Ms is { } p95 && summary is { P95Ms: var actual95 } && actual95 > p95)
                failures.Add($"{metric}: p95 {actual95:F1}ms exceeds threshold {p95:F1}ms");
            if (thresh.P99Ms is { } p99 && summary is { P99Ms: var actual99 } && actual99 > p99)
                failures.Add($"{metric}: p99 {actual99:F1}ms exceeds threshold {p99:F1}ms");

            if (thresh.MaxErrorRate is { } maxRate && total > 0)
            {
                var rate = (double)errCount / total;
                if (rate > maxRate)
                {
                    failures.Add($"{metric}: error rate {rate:P2} exceeds threshold {maxRate:P2} ({errCount}/{total})");
                }
            }

            if (thresh.MinSamples is { } minSamples && (summary?.Count ?? 0) < minSamples)
            {
                failures.Add($"{metric}: only {summary?.Count ?? 0} samples, threshold requires ≥{minSamples}");
            }
        }

        return new GateResult(failures.Count == 0, failures);
    }
}

public sealed class MetricThreshold
{
    public double? P50Ms { get; set; }
    public double? P95Ms { get; set; }
    public double? P99Ms { get; set; }
    public double? MaxErrorRate { get; set; }
    public long? MinSamples { get; set; }
}

public sealed record GateResult(bool Passed, IReadOnlyList<string> Failures);
