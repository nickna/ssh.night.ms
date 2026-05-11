using System.Drawing;

namespace Night.Ms.SshServer.Tui.Drivers;

// The Terminal.Gui DriverRegistry expects a parameterless factory delegate, but our SSH driver
// needs per-session state (the channel streams + PTY size). We bridge by stashing a Value into
// AsyncLocal before Application.Init runs, so the factory's parameterless ctor can read it.
internal sealed class SshChannelDriverContext
{
    private static readonly AsyncLocal<SshChannelDriverContext?> Current = new();

    public required Stream Input { get; init; }
    public required Stream Output { get; init; }
    public required Func<Size> GetSize { get; init; }

    public static SshChannelDriverContext CurrentOrThrow =>
        Current.Value ?? throw new InvalidOperationException(
            "No SshChannelDriverContext on the current async flow. Wrap Application.Init in a SshChannelDriverContext.Scope.");

    public static IDisposable Push(SshChannelDriverContext context)
    {
        var previous = Current.Value;
        Current.Value = context;
        return new Popper(previous);
    }

    private sealed class Popper(SshChannelDriverContext? previous) : IDisposable
    {
        public void Dispose() => Current.Value = previous;
    }
}
