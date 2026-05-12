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

        // Default channel for chat — seeded by DatabaseInitializer as #lobby.
        Channel? lobbyChannel = await db.Channels
            .FirstOrDefaultAsync(c => c.Name == "lobby", cancellationToken);

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
                using var app = (IApplication)ApplicationImplCtor.Value.Invoke([factory, new SystemTimeProvider()]);
                app.Init();

                var art = scope.ServiceProvider.GetRequiredService<ArtProvider>();

                if (session.AuthDecision is AuthDecision.Unknown)
                {
                    var sysopBootstrap = scope.ServiceProvider.GetRequiredService<Auth.SysopBootstrap>();
                    var registerResult = app.Run(new RegisterScreen(app, scope.ServiceProvider, session, db, sysopBootstrap, art));
                    if (registerResult is User registered)
                    {
                        user = registered;
                        justRegistered = true;
                        logger.LogInformation("Registered new account: handle={Handle} fingerprint={Fingerprint}",
                            registered.Handle, session.Fingerprint);
                    }
                    else
                    {
                        return; // user cancelled registration
                    }
                }

                if (user is null)
                {
                    logger.LogWarning("AuthDecision.Known but user row missing for fingerprint={Fingerprint}", session.Fingerprint);
                    return;
                }

                RunLobbyLoop(services, app, user, justRegistered, lobbyChannel, art, session);
            }
            catch (Exception ex)
            {
                logger.LogError(ex, "Terminal.Gui session crashed for fingerprint={Fingerprint}", session.Fingerprint);
            }
        }, cancellationToken).ConfigureAwait(false);
    }

    private static void RunLobbyLoop(IServiceProvider services, IApplication app, User user, bool justRegistered, Channel? lobbyChannel, ArtProvider art, BbsSession session)
    {
        var nav = (LobbyNavigation?)app.Run(new LobbyScreen(app, services, user, justRegistered, art));
        while (nav is LobbyNavigation.Chat or LobbyNavigation.Boards or LobbyNavigation.Profile or LobbyNavigation.News or LobbyNavigation.Browser or LobbyNavigation.Gallery or LobbyNavigation.Sysop)
        {
            if (nav == LobbyNavigation.Chat && lobbyChannel is not null)
            {
                app.Run(new ChatScreen(services, app, user, lobbyChannel));
            }
            else if (nav == LobbyNavigation.Boards)
            {
                RunForumLoop(services, app, user);
            }
            else if (nav == LobbyNavigation.Profile)
            {
                app.Run(new ProfileEditScreen(app, services, user, session.RemoteIPAddress));
            }
            else if (nav == LobbyNavigation.News)
            {
                app.Run(new NewsScreen(services, app, user));
            }
            else if (nav == LobbyNavigation.Browser)
            {
                if (app.Run(new BrowserPromptScreen(app, services, user)) is Uri uri)
                {
                    app.Run(new ReaderScreen(app, services, user, uri));
                }
            }
            else if (nav == LobbyNavigation.Gallery)
            {
                var gallery = services.GetRequiredService<Night.Ms.SshServer.Tui.Art.IArtGalleryProvider>();
                app.Run(new GalleryScreen(app, services, user, gallery));
            }
            else if (nav == LobbyNavigation.Sysop && user.IsSysop)
            {
                app.Run(new AdminScreen(services, app, user));
            }
            nav = (LobbyNavigation?)app.Run(new LobbyScreen(app, services, user, justRegistered: false, art));
        }
    }

    private static void RunForumLoop(IServiceProvider services, IApplication app, User user)
    {
        while (true)
        {
            Forum? forum;
            using (var scope = services.CreateScope())
            {
                var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
                forum = app.Run(new ForumListScreen(app, services, db, user)) as Forum;
            }
            if (forum is null) return;

            while (true)
            {
                TopicListScreen topicListScreen;
                using (var scope = services.CreateScope())
                {
                    var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
                    topicListScreen = new TopicListScreen(app, services, db, user, forum);
                }
                var listResult = (TopicListResult?)app.Run(topicListScreen);
                if (listResult == TopicListResult.Back) break;

                Topic? topic = null;
                if (listResult == TopicListResult.OpenTopic)
                {
                    topic = topicListScreen.SelectedTopic;
                }
                else if (listResult == TopicListResult.NewTopic)
                {
                    using var scope = services.CreateScope();
                    var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
                    topic = app.Run(new NewTopicScreen(app, services, db, user, forum)) as Topic;
                }
                if (topic is null) continue; // back to topic list

                app.Run(new ThreadScreen(services, app, user, topic));
            }
        }
    }

    private static Size GetPtySize(BbsSession session)
    {
        var cols = (int)(session.Pty?.Cols ?? 80);
        var rows = (int)(session.Pty?.Rows ?? 24);
        return new Size(Math.Max(cols, 20), Math.Max(rows, 5));
    }
}
