using System.Globalization;
using System.Text;
using System.Text.Json;

namespace Night.Ms.Tools.LoadTest.Metrics;

// Writes the final report out three ways: a human-readable table on stdout (always),
// a CSV one row per metric (diff-friendly across runs), and a structured JSON dump
// (machine-readable, includes the run metadata in addition to the metrics).
public static class Report
{
    public static void PrintTable(MetricsCollector metrics, RunMetadata meta)
    {
        var latencies = metrics.SummarizeLatencies();
        var errors = metrics.SummarizeErrors();

        Console.Out.WriteLine();
        Console.Out.WriteLine($"loadtest report — {meta.Scenarios} — N={meta.BotCount} ramp={meta.RampSeconds}s duration={meta.DurationSeconds}s");
        Console.Out.WriteLine(new string('─', 96));
        Console.Out.WriteLine($"{"metric",-32} {"count",10} {"p50",10} {"p95",10} {"p99",10} {"max",10} {"errors",8}");
        foreach (var key in latencies.Keys.OrderBy(k => k, StringComparer.Ordinal))
        {
            var s = latencies[key];
            errors.TryGetValue(key, out var err);
            Console.Out.WriteLine(
                $"{key,-32} {s.Count,10:N0} {s.P50Ms,8:N1}ms {s.P95Ms,8:N1}ms {s.P99Ms,8:N1}ms {s.MaxMs,8:N1}ms {err,8:N0}");
        }
        // Surface error-only metrics (e.g., a failure in connect that never produced a timing).
        foreach (var key in errors.Keys.Where(k => !latencies.ContainsKey(k)).OrderBy(k => k, StringComparer.Ordinal))
        {
            Console.Out.WriteLine($"{key,-32} {"-",10} {"-",10} {"-",10} {"-",10} {"-",10} {errors[key],8:N0}");
        }
        Console.Out.WriteLine();
    }

    public static void WriteCsv(string path, MetricsCollector metrics)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        var latencies = metrics.SummarizeLatencies();
        var errors = metrics.SummarizeErrors();
        var sb = new StringBuilder();
        sb.AppendLine("metric,count,p50_ms,p95_ms,p99_ms,max_ms,mean_ms,errors");
        var keys = latencies.Keys.Concat(errors.Keys).Distinct().OrderBy(k => k, StringComparer.Ordinal);
        foreach (var key in keys)
        {
            latencies.TryGetValue(key, out var s);
            errors.TryGetValue(key, out var err);
            var fmt = CultureInfo.InvariantCulture;
            if (s is not null)
            {
                sb.AppendFormat(fmt, "{0},{1},{2:F3},{3:F3},{4:F3},{5:F3},{6:F3},{7}\n",
                    key, s.Count, s.P50Ms, s.P95Ms, s.P99Ms, s.MaxMs, s.MeanMs, err);
            }
            else
            {
                sb.AppendFormat(fmt, "{0},0,,,,,,,{1}\n", key, err);
            }
        }
        File.WriteAllText(path, sb.ToString());
    }

    public static void WriteJson(string path, MetricsCollector metrics, RunMetadata meta)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        var doc = new
        {
            metadata = meta,
            latencies = metrics.SummarizeLatencies(),
            errors = metrics.SummarizeErrors(),
        };
        File.WriteAllText(path, JsonSerializer.Serialize(doc, new JsonSerializerOptions { WriteIndented = true }));
    }
}

public sealed record RunMetadata(
    DateTimeOffset StartedAt,
    DateTimeOffset FinishedAt,
    int BotCount,
    int RampSeconds,
    int DurationSeconds,
    string Host,
    int Port,
    string Scenarios);
