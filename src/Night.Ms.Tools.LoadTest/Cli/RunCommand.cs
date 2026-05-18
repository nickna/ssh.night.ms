namespace Night.Ms.Tools.LoadTest.Cli;

// `run` — stub. Real driver lands in Phase 2.
internal static class RunCommand
{
    public static Task<int> RunAsync(CancellationToken ct)
    {
        Console.Error.WriteLine("loadtest run: not yet implemented (Phase 2).");
        return Task.FromResult(2);
    }
}
