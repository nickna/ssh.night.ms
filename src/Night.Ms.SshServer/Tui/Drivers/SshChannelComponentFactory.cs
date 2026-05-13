using System.Collections.Concurrent;
using Terminal.Gui.Drivers;
using Terminal.Gui.Time;

namespace Night.Ms.SshServer.Tui.Drivers;

internal sealed class SshChannelComponentFactory : ComponentFactoryImpl<char>
{
    public const string DriverName = "ssh-channel";

    private readonly SshChannelDriverContext _context;

    // DriverRegistry calls this parameterless constructor — we hydrate from AsyncLocal context
    // pushed by the session runner before calling Application.Init.
    public SshChannelComponentFactory()
        : this(SshChannelDriverContext.CurrentOrThrow)
    {
    }

    private SshChannelComponentFactory(SshChannelDriverContext context)
    {
        _context = context;
    }

    public override string GetDriverName() => DriverName;

    public override IInput<char> CreateInput() => new SshChannelInput(_context.Input);

    public override IInputProcessor CreateInputProcessor(ConcurrentQueue<char> inputBuffer, ITimeProvider? timeProvider = null)
        // AnsiInputProcessor handles xterm sequence parsing (keys + SGR mouse) for arbitrary char streams.
        => new AnsiInputProcessor(inputBuffer, timeProvider);

    public override IOutput CreateOutput() => new SshChannelOutput(_context.Output, _context.GetSize);
}
