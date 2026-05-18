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

          run    [more flags in Phase 2]

          clean  Delete all loadbot-* users (cascade drops their SSH credentials).

        Connection string is read from ConnectionStrings__bbs (matches run.ps1).
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
                "run" => await RunCommand.RunAsync(cts.Token),
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
        var keysDir = Path.Combine(AppContext.BaseDirectory, "loadtest-keys");
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

    private static int Fail(string message)
    {
        Console.Error.WriteLine($"loadtest: {message}");
        Console.Error.WriteLine();
        Console.Error.WriteLine(Usage);
        return 1;
    }
}
