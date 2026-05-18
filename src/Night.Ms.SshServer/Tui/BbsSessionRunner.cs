using System.Drawing;
using System.Reflection;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Drivers;
using Night.Ms.SshServer.Tui.Screens;
using Night.Ms.SshTransport;
using Terminal.Gui.App;
using Terminal.Gui.Drivers;
using Terminal.Gui.Time;
using Terminal.Gui.Views;

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
        ITuiSession session,
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

                if (session.AuthDecision is AuthDecision.SignupRequired signup)
                {
                    // SSH-side signup: SSH transport handed us a SignupRequired with the
                    // requested handle prefilled. The offered key (if any) lives on the
                    // BbsSession's Offered* fields — exposed via the SshSessionAdapter.
                    // Web sessions are gated to Authorize at /ws/bbs and synthesize
                    // AuthDecision.Known — they never hit this path. If they ever do, fail
                    // loudly rather than render the wrong screen with empty credentials.
                    if (session is not SshSessionAdapter ssh)
                    {
                        logger.LogError("AuthDecision.SignupRequired reached a non-SSH session ({Session}); refusing to register.", session.DisplayName);
                        return;
                    }
                    var sysopBootstrap = scope.ServiceProvider.GetRequiredService<Auth.SysopBootstrap>();
                    var passwordHasher = scope.ServiceProvider.GetRequiredService<Auth.IPasswordHasher>();
                    var nightMsOptions = scope.ServiceProvider.GetRequiredService<Configuration.NightMsOptions>();
                    var registerResult = app.Run(new RegisterScreen(app, scope.ServiceProvider,
                        signup, ssh.Inner.OfferedFingerprint, ssh.Inner.OfferedAlgorithm, ssh.Inner.OfferedBlob,
                        db, sysopBootstrap, passwordHasher, nightMsOptions, art));
                    if (registerResult is User registered)
                    {
                        user = registered;
                        justRegistered = true;
                        logger.LogInformation("Registered new account: handle={Handle} session={Session}",
                            registered.Handle, session.DisplayName);
                    }
                    else
                    {
                        return; // user cancelled registration
                    }
                }

                if (user is null)
                {
                    logger.LogWarning("AuthDecision.Known but user row missing for session={Session}", session.DisplayName);
                    return;
                }

                // Adopt-key prompt: if the user logged in via password (or signed up) and
                // their session carried an SSH key the account doesn't yet know about, ask
                // before dropping them into the lobby. Skip if just-registered with adoption
                // already done in RegisterScreen, if the key isn't actually unknown to this
                // user, or if they previously chose "Never for this key".
                if (!justRegistered && session is SshSessionAdapter adopter)
                {
                    MaybeRunKeyAdoptionPrompt(services, app, user, adopter, db, logger);
                }

                RunLobbyLoop(services, app, user, justRegistered, lobbyChannel, art, session);
            }
            catch (Exception ex)
            {
                logger.LogError(ex, "Terminal.Gui session crashed for session={Session}", session.DisplayName);
            }
        }, cancellationToken).ConfigureAwait(false);
    }

    // Synchronous wrapper around the adopt-key flow. Runs on the Terminal.Gui UI thread —
    // db queries here block, but they're small and uncontended at session-start time.
    private static void MaybeRunKeyAdoptionPrompt(IServiceProvider services, IApplication app, User user, SshSessionAdapter adapter, AppDbContext db, ILogger logger)
    {
        var fingerprint = adapter.Inner.OfferedFingerprint;
        var algorithm = adapter.Inner.OfferedAlgorithm;
        var blob = adapter.Inner.OfferedBlob;
        if (string.IsNullOrEmpty(fingerprint) || string.IsNullOrEmpty(algorithm) || blob is null || blob.Length == 0)
        {
            return;
        }

        try
        {
            // Global preference short-circuits before the Redis roundtrip. User-set toggle
            // from the profile Settings tab; flips back to default when the user re-enables.
            if (user.SuppressKeyAdoptionPrompts) return;

            var alreadyOnAccount = db.IdentityCredentials.Any(c =>
                c.UserId == user.Id && c.Provider == CredentialProvider.Ssh && c.Subject == fingerprint);
            if (alreadyOnAccount) return;

            var dismissals = services.GetRequiredService<Auth.IDismissedKeyStore>();
            // Dismissals stored in Redis — synchronous wait here is intentional. Block size
            // is one tiny key roundtrip; the UI thread is already idle at lobby entry.
            if (dismissals.IsDismissedAsync(user.Id, fingerprint, default).GetAwaiter().GetResult()) return;

            var prompt = new KeyAdoptionPrompt(app, services, user, fingerprint, algorithm, blob, db, dismissals);
            app.Run(prompt);
        }
        catch (Exception ex)
        {
            // Adoption is a nice-to-have, never block login on its failure.
            logger.LogWarning(ex, "Adopt-key prompt failed for user={Handle}", user.Handle);
        }
    }

    private static void RunLobbyLoop(IServiceProvider services, IApplication app, User user, bool justRegistered, Channel? lobbyChannel, ArtProvider art, ITuiSession session)
    {
        // Register with the singleton WallDispatcher instead of opening our own Redis
        // subscription. The dispatcher subscribes once for the whole process and invokes
        // our callback per broadcast; disposing the token on lobby exit unregisters.
        var wallDispatcher = services.GetRequiredService<WallDispatcher>();
        using var wallSubscription = wallDispatcher.Subscribe(dto =>
        {
            app.Invoke(() =>
            {
                MessageBox.Query(
                    app,
                    title: "System Broadcast",
                    message: $"From sysop {dto.SysopHandle}:\n\n{dto.Message}",
                    "_OK");
            });
        });

        var lobbyScreen = new LobbyScreen(app, services, user, justRegistered, art);
        var nav = RunChild(app, lobbyScreen, out var lobbyResult) ?? (LobbyNavigation?)lobbyResult;
        while (nav is LobbyNavigation.Chat or LobbyNavigation.Boards or LobbyNavigation.Profile or LobbyNavigation.News or LobbyNavigation.Browser or LobbyNavigation.Gallery or LobbyNavigation.Map or LobbyNavigation.Weather or LobbyNavigation.Alerts or LobbyNavigation.Finance or LobbyNavigation.Doors or LobbyNavigation.Sysop)
        {
            if (nav == LobbyNavigation.Chat && lobbyChannel is not null)
            {
                if (RunChild(app, new ChatScreen(services, app, user, lobbyChannel), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Boards)
            {
                if (RunForumLoop(services, app, user) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Profile)
            {
                if (RunChild(app, new ProfileEditScreen(app, services, user, session.RemoteIPAddress), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.News)
            {
                if (RunChild(app, new NewsScreen(services, app, user), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Browser)
            {
                if (RunChild(app, new BrowserPromptScreen(app, services, user), out var promptResult) is { } shortcut) { nav = shortcut; continue; }
                if (promptResult is Uri uri)
                {
                    if (RunChild(app, new ReaderScreen(app, services, user, uri), out _) is { } readerShortcut) { nav = readerShortcut; continue; }
                }
            }
            else if (nav == LobbyNavigation.Gallery)
            {
                var gallery = services.GetRequiredService<Night.Ms.SshServer.Tui.Art.IArtGalleryProvider>();
                if (RunChild(app, new GalleryScreen(app, services, user, gallery), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Map)
            {
                var rasterTiles = services.GetRequiredService<Night.Ms.SshServer.Tui.Map.IOsmTileFetcher>();
                var vectorTiles = services.GetRequiredService<Night.Ms.SshServer.Tui.Map.IVectorTileFetcher>();
                var logger = services.GetRequiredService<ILogger<MapScreen>>();
                if (RunChild(app, new MapScreen(app, services, user, rasterTiles, vectorTiles, logger), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Weather)
            {
                using var scope = services.CreateScope();
                app.Run(new WeatherScreen(app, scope.ServiceProvider, user));
            }
            else if (nav == LobbyNavigation.Finance)
            {
                using var scope = services.CreateScope();
                if (RunChild(app, new FinanceScreen(app, scope.ServiceProvider, user), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Alerts)
            {
                var alerts = lobbyScreen.LoadedAlerts;
                if (alerts is { Count: > 0 })
                {
                    if (RunChild(app, new AlertsScreen(app, services, user, alerts), out _) is { } shortcut) { nav = shortcut; continue; }
                }
            }
            else if (nav == LobbyNavigation.Doors)
            {
                if (RunChild(app, new DoorsScreen(app, services, user), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Sysop && user.IsSysop)
            {
                if (RunChild(app, new AdminScreen(services, app, user), out _) is { } shortcut) { nav = shortcut; continue; }
            }
            lobbyScreen = new LobbyScreen(app, services, user, justRegistered: false, art);
            nav = RunChild(app, lobbyScreen, out var nextLobbyResult) ?? (LobbyNavigation?)nextLobbyResult;
        }
    }

    // Runs a child screen and returns the footer-weather shortcut if the user clicked the
    // footer; otherwise null. The screen's own typed return value (from app.Run) is
    // surfaced through `typedResult` for callers that need it; most callers discard it.
    private static LobbyNavigation? RunChild(IApplication app, BbsWindow screen, out object? typedResult)
    {
        typedResult = app.Run(screen);
        return screen.FooterShortcutResult;
    }

    // Returns null when the user backs out of the forum loop normally; returns a
    // LobbyNavigation when a footer-click shortcut on one of the inner screens needs to
    // bubble up to RunLobbyLoop for cross-screen dispatch.
    private static LobbyNavigation? RunForumLoop(IServiceProvider services, IApplication app, User user)
    {
        while (true)
        {
            ForumListScreen forumListScreen;
            using (var scope = services.CreateScope())
            {
                var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
                forumListScreen = new ForumListScreen(app, services, db, user);
            }
            if (RunChild(app, forumListScreen, out var forumResult) is { } s1) return s1;
            var forum = forumResult as Forum;
            if (forum is null) return null;

            while (true)
            {
                TopicListScreen topicListScreen;
                using (var scope = services.CreateScope())
                {
                    var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
                    topicListScreen = new TopicListScreen(app, services, db, user, forum);
                }
                if (RunChild(app, topicListScreen, out var listObj) is { } s2) return s2;
                var listResult = (TopicListResult?)listObj;
                if (listResult == TopicListResult.Back) break;

                Topic? topic = null;
                if (listResult == TopicListResult.OpenTopic)
                {
                    topic = topicListScreen.SelectedTopic;
                }
                else if (listResult == TopicListResult.NewTopic)
                {
                    if (RunChild(app, new NewTopicScreen(app, services, user, forum), out var newTopicResult) is { } s3) return s3;
                    topic = newTopicResult as Topic;
                }
                if (topic is null) continue; // back to topic list

                if (RunChild(app, new ThreadScreen(services, app, user, topic), out _) is { } s4) return s4;
            }
        }
    }

    private static Size GetPtySize(ITuiSession session)
    {
        var cols = (int)(session.Pty?.Cols ?? 80);
        var rows = (int)(session.Pty?.Rows ?? 24);
        return new Size(Math.Max(cols, 20), Math.Max(rows, 5));
    }
}
