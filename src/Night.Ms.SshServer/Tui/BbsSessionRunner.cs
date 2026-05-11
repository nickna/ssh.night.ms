using System.Drawing;
using System.Reflection;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
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

    public static async Task RunAsync(
        IServiceProvider services,
        BbsSession session,
        ILogger logger,
        CancellationToken cancellationToken)
    {
        await using var scope = services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();

        // Resolve the user and decide which screen path to enter. For Known sessions we update
        // last-seen up front so chat presence reflects connect time rather than logout time.
        User? user = null;
        var justRegistered = false;
        if (session.AuthDecision is AuthDecision.Known known)
        {
            user = await db.Users.FirstOrDefaultAsync(u => u.Id == known.UserId, cancellationToken);
            if (user is not null)
            {
                user.LastSeenAt = DateTimeOffset.UtcNow;
                await db.SaveChangesAsync(cancellationToken);
            }
        }

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
                if (session.AuthDecision is AuthDecision.Unknown)
                {
                    user = RunRegister(session, db, logger);
                    justRegistered = user is not null;
                    if (user is null) return; // they cancelled registration
                }

                if (user is null)
                {
                    // Known auth but DB row missing — extremely unlikely, but bail safely.
                    logger.LogWarning("AuthDecision.Known but user row missing for fingerprint={Fingerprint}", session.Fingerprint);
                    return;
                }

                RunLobby(user, justRegistered);
            }
            catch (Exception ex)
            {
                logger.LogError(ex, "Terminal.Gui session crashed for fingerprint={Fingerprint}", session.Fingerprint);
            }
        }, cancellationToken).ConfigureAwait(false);
    }

    private static User? RunRegister(BbsSession session, AppDbContext db, ILogger logger)
    {
        var factory = new SshChannelComponentFactory();
        var app = (IApplication)ApplicationImplCtor.Value.Invoke([factory, new SystemTimeProvider()]);
        using (app)
        {
            app.Init();
            var screen = new RegisterScreen(session, db);
            var result = app.Run(screen, _ => true);
            if (result is User user)
            {
                logger.LogInformation("Registered new account: handle={Handle} fingerprint={Fingerprint}",
                    user.Handle, session.Fingerprint);
                return user;
            }
            return null;
        }
    }

    private static void RunLobby(User user, bool justRegistered)
    {
        var factory = new SshChannelComponentFactory();
        var app = (IApplication)ApplicationImplCtor.Value.Invoke([factory, new SystemTimeProvider()]);
        using (app)
        {
            app.Init();
            var screen = new LobbyScreen(user, justRegistered);
            app.Run(screen, _ => true);
        }
    }

    private static Size GetPtySize(BbsSession session)
    {
        var cols = (int)(session.Pty?.Cols ?? 80);
        var rows = (int)(session.Pty?.Rows ?? 24);
        return new Size(Math.Max(cols, 20), Math.Max(rows, 5));
    }
}
