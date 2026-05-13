using Microsoft.Extensions.DependencyInjection;

namespace Night.Ms.SshServer.Tui;

// Replaces the bare `_ = SomeAsync()` pattern that silently swallows exceptions on the
// fire-and-forget path. Failures are logged with the provided context tag so a screen-side
// background task that throws shows up in logs instead of vanishing.
//
// OperationCanceledException is treated as expected (screens cancel _shutdown on close) and
// is not logged.
internal static class FireAndForget
{
    public static void FireAndLog(this Task task, ILogger logger, string context)
    {
        _ = ObserveAsync(task, logger, context);
    }

    public static void FireAndLog(this Task task, IServiceProvider services, string context)
    {
        var logger = services.GetRequiredService<ILoggerFactory>().CreateLogger("ssh.night.ms.Tui");
        _ = ObserveAsync(task, logger, context);
    }

    private static async Task ObserveAsync(Task task, ILogger logger, string context)
    {
        try
        {
            await task.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // expected on screen teardown / channel switch
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Background task '{Context}' failed", context);
        }
    }
}
