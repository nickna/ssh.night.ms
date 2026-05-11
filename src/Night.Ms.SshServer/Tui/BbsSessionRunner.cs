using System.Drawing;
using System.Reflection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Tui.Drivers;
using Night.Ms.SshServer.Tui.Screens;
using Night.Ms.SshTransport;
using Terminal.Gui.App;
using Terminal.Gui.Drivers;
using Terminal.Gui.Time;

namespace Night.Ms.SshServer.Tui;

internal static class BbsSessionRunner
{
    // Terminal.Gui v2's ApplicationImpl(IComponentFactory) constructor is internal — the only
    // public path (Application.Create) routes through a hardcoded switch that knows just the
    // built-in driver names. Reflect to call it directly until upstream exposes a public hook.
    private static readonly Lazy<ConstructorInfo> ApplicationImplCtor = new(() =>
    {
        var asm = typeof(IApplication).Assembly;
        var implType = asm.GetType("Terminal.Gui.App.ApplicationImpl")
            ?? throw new InvalidOperationException("Terminal.Gui.App.ApplicationImpl not found.");
        var ctor = implType.GetConstructor(
            BindingFlags.NonPublic | BindingFlags.Instance,
            binder: null,
            types: [typeof(IComponentFactory), typeof(ITimeProvider)],
            modifiers: null);
        return ctor ?? throw new InvalidOperationException(
            "ApplicationImpl(IComponentFactory, ITimeProvider) constructor not found — Terminal.Gui internals changed.");
    });

    public static async Task RunAsync(BbsSession session, ILogger logger, CancellationToken cancellationToken)
    {
        // Terminal.Gui owns its own thread for the input pump + main loop. Spin up the
        // Application on a dedicated thread so blocking inside Run() doesn't tie up the
        // SSH session handler.
        await Task.Run(() =>
        {
            using var contextScope = SshChannelDriverContext.Push(new SshChannelDriverContext
            {
                Input = session.Stream,
                Output = session.Stream,
                GetSize = () => GetPtySize(session),
            });

            try
            {
                var factory = new SshChannelComponentFactory();
                var timeProvider = new SystemTimeProvider();
                var app = (IApplication)ApplicationImplCtor.Value.Invoke([factory, timeProvider]);
                using (app)
                {
                    app.Init();
                    var screen = new HelloScreen("guest", session.KeyAlgorithm, session.Fingerprint);
                    app.Run(screen, _ => true);
                }
            }
            catch (Exception ex)
            {
                logger.LogError(ex, "Terminal.Gui session crashed for fingerprint={Fingerprint}", session.Fingerprint);
            }
        }, cancellationToken).ConfigureAwait(false);
    }

    private static Size GetPtySize(BbsSession session)
    {
        var cols = (int)(session.Pty?.Cols ?? 80);
        var rows = (int)(session.Pty?.Rows ?? 24);
        return new Size(Math.Max(cols, 20), Math.Max(rows, 5));
    }
}
