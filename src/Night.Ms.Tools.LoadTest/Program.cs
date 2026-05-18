using Night.Ms.Tools.LoadTest.Cli;

namespace Night.Ms.Tools.LoadTest;

internal static class Program
{
    private const string Usage = """
        Night.Ms.Tools.LoadTest — synthetic SSH load harness.

        Subcommands:
          seed   --count N [--keys-dir <path>]
                 Generate N keypairs (or reuse existing in keys-dir) and upsert
                 loadbot-NNNN users + identity_credentials rows. Idempotent.

          run    --count N [--host <h>] [--port <p>] [--ramp-seconds <s>]
                 [--duration-seconds <s>] [--keys-dir <path>] [--reports-dir <path>]
                 Open N SSH sessions, run the scenario mix for the configured
                 duration, write stdout table + CSV + JSON report.

          clean  Delete all loadbot-* users (cascade drops their SSH credentials).

        Defaults:
          --host             localhost
          --port             2222
          --ramp-seconds     30
          --duration-seconds 300
          --keys-dir         ./loadtest-keys (relative to the tool binary)
          --reports-dir      ./loadtest-reports

        Connection string for `seed`/`clean` is read from ConnectionStrings__bbs
        (matches run.ps1).
        """;

    private static async Task<int> Main(string[] args)
    {
        using var cts = new CancellationTokenSource();
        Console.CancelKeyPress += (_, e) => { e.Cancel = true; cts.Cancel(); };

        if (args.Length == 0 || args[0] is "-h" or "--help")
        {
            Console.Error.WriteLine(Usage);
            return args.Length == 0 ? 1 : 0;
        }

        try
        {
            return args[0] switch
            {
                "seed" => await RunSeed(args[1..], cts.Token),
                "run" => await RunRun(args[1..], cts.Token),
                "clean" => await CleanCommand.RunAsync(cts.Token),
                _ => Fail($"Unknown subcommand: {args[0]}"),
            };
        }
        catch (OperationCanceledException)
        {
            Console.Error.WriteLine("loadtest: cancelled.");
            return 130;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"loadtest: {ex.Message}");
            return 1;
        }
    }

    private static async Task<int> RunSeed(string[] args, CancellationToken ct)
    {
        var count = 0;
        var keysDir = DefaultKeysDir();
        for (var i = 0; i < args.Length; i++)
        {
            switch (args[i])
            {
                case "--count":
                    if (++i >= args.Length || !int.TryParse(args[i], out count) || count <= 0)
                        return Fail("--count expects a positive integer.");
                    break;
                case "--keys-dir":
                    if (++i >= args.Length) return Fail("--keys-dir expects a path.");
                    keysDir = args[i];
                    break;
                default:
                    return Fail($"seed: unexpected argument: {args[i]}");
            }
        }
        if (count == 0) return Fail("seed: --count is required.");
        return await SeedCommand.RunAsync(count, keysDir, ct);
    }

    private static async Task<int> RunRun(string[] args, CancellationToken ct)
    {
        var host = "localhost";
        var port = 2222;
        var count = 0;
        var ramp = 30;
        var duration = 300;
        var keysDir = DefaultKeysDir();
        var reportsDir = Path.Combine(AppContext.BaseDirectory, "loadtest-reports");
        for (var i = 0; i < args.Length; i++)
        {
            switch (args[i])
            {
                case "--host": if (++i >= args.Length) return Fail("--host expects a value."); host = args[i]; break;
                case "--port":
                    if (++i >= args.Length || !int.TryParse(args[i], out port)) return Fail("--port expects an integer.");
                    break;
                case "--count":
                    if (++i >= args.Length || !int.TryParse(args[i], out count) || count <= 0) return Fail("--count expects a positive integer.");
                    break;
                case "--ramp-seconds":
                    if (++i >= args.Length || !int.TryParse(args[i], out ramp) || ramp < 0) return Fail("--ramp-seconds expects a non-negative integer.");
                    break;
                case "--duration-seconds":
                    if (++i >= args.Length || !int.TryParse(args[i], out duration) || duration <= 0) return Fail("--duration-seconds expects a positive integer.");
                    break;
                case "--keys-dir": if (++i >= args.Length) return Fail("--keys-dir expects a path."); keysDir = args[i]; break;
                case "--reports-dir": if (++i >= args.Length) return Fail("--reports-dir expects a path."); reportsDir = args[i]; break;
                default: return Fail($"run: unexpected argument: {args[i]}");
            }
        }
        if (count == 0) return Fail("run: --count is required.");
        return await RunCommand.RunAsync(new RunOptions(host, port, count, ramp, duration, keysDir, reportsDir), ct);
    }

    private static string DefaultKeysDir() => Path.Combine(AppContext.BaseDirectory, "loadtest-keys");

    private static int Fail(string message)
    {
        Console.Error.WriteLine($"loadtest: {message}");
        Console.Error.WriteLine();
        Console.Error.WriteLine(Usage);
        return 1;
    }
}
