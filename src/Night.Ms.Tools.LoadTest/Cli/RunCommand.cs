using Night.Ms.Tools.LoadTest.Bots;
using Night.Ms.Tools.LoadTest.Driver;
using Night.Ms.Tools.LoadTest.Metrics;

namespace Night.Ms.Tools.LoadTest.Cli;

internal sealed record RunOptions(
    string Host,
    int Port,
    int Count,
    int RampSeconds,
    int DurationSeconds,
    string KeysDir,
    string ReportsDir);

// `run` — opens N SSH sessions against the configured host, runs the assigned scenarios
// for the configured duration, writes a stdout table + CSV + JSON report.
internal static class RunCommand
{
    public static async Task<int> RunAsync(RunOptions opts, CancellationToken ct)
    {
        if (opts.Count <= 0) { Console.Error.WriteLine("loadtest run: --count must be positive."); return 1; }
        if (opts.RampSeconds < 0) { Console.Error.WriteLine("loadtest run: --ramp-seconds cannot be negative."); return 1; }
        if (opts.DurationSeconds <= 0) { Console.Error.WriteLine("loadtest run: --duration-seconds must be positive."); return 1; }

        if (!Directory.Exists(opts.KeysDir) || Directory.GetFiles(opts.KeysDir, "loadbot-*.pem").Length < opts.Count)
        {
            Console.Error.WriteLine($"loadtest run: not enough keys in {opts.KeysDir} for --count={opts.Count}. Run `seed --count {opts.Count}` first.");
            return 1;
        }

        var keyStore = new BotKeyStore(opts.KeysDir);
        var assignment = ProfileAssignment.AllIdle();
        var driverConfig = new DriverConfig(opts.Host, opts.Port, opts.Count, opts.RampSeconds, opts.DurationSeconds);
        var driver = new Driver.Driver(driverConfig, keyStore, assignment.For);

        var startedAt = DateTimeOffset.UtcNow;
        await driver.RunAsync(ct).ConfigureAwait(false);
        var finishedAt = DateTimeOffset.UtcNow;

        var meta = new RunMetadata(
            StartedAt: startedAt,
            FinishedAt: finishedAt,
            BotCount: opts.Count,
            RampSeconds: opts.RampSeconds,
            DurationSeconds: opts.DurationSeconds,
            Host: opts.Host,
            Port: opts.Port,
            Scenarios: "idle");

        Report.PrintTable(driver.Metrics, meta);
        var stamp = startedAt.ToString("yyyyMMdd-HHmmss");
        var csvPath = Path.Combine(opts.ReportsDir, $"loadtest-{stamp}.csv");
        var jsonPath = Path.Combine(opts.ReportsDir, $"loadtest-{stamp}.json");
        Report.WriteCsv(csvPath, driver.Metrics);
        Report.WriteJson(jsonPath, driver.Metrics, meta);
        Console.Out.WriteLine($"loadtest: wrote {csvPath}");
        Console.Out.WriteLine($"loadtest: wrote {jsonPath}");
        return 0;
    }
}
