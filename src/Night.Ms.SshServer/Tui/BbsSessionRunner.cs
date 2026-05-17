using System.Drawing;
using System.Reflection;
using System.Text.Json;
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
        using var wallCts = new CancellationTokenSource();
        RunWallSubscriptionAsync(services, app, wallCts.Token)
            .FireAndLog(services, nameof(RunWallSubscriptionAsync));

        try
        {
        var lobbyScreen = new LobbyScreen(app, services, user, justRegistered, art);
        lobbyScreen.EnableFooterWeatherShortcut();
        var lobbyResult = app.Run(lobbyScreen);
        // Footer-click shortcut wins over the screen's own return value — see BbsWindow.FooterShortcutResult.
        var nav = lobbyScreen.FooterShortcutResult ?? (LobbyNavigation?)lobbyResult;
        while (nav is LobbyNavigation.Chat or LobbyNavigation.Boards or LobbyNavigation.Profile or LobbyNavigation.News or LobbyNavigation.Browser or LobbyNavigation.Gallery or LobbyNavigation.Map or LobbyNavigation.Weather or LobbyNavigation.Alerts or LobbyNavigation.Finance or LobbyNavigation.Sysop)
        {
            if (nav == LobbyNavigation.Chat && lobbyChannel is not null)
            {
                var screen = new ChatScreen(services, app, user, lobbyChannel);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Boards)
            {
                if (RunForumLoop(services, app, user) is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Profile)
            {
                var screen = new ProfileEditScreen(app, services, user, session.RemoteIPAddress);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.News)
            {
                var screen = new NewsScreen(services, app, user);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Browser)
            {
                var prompt = new BrowserPromptScreen(app, services, user);
                prompt.EnableFooterWeatherShortcut();
                var promptResult = app.Run(prompt);
                if (prompt.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
                if (promptResult is Uri uri)
                {
                    var reader = new ReaderScreen(app, services, user, uri);
                    reader.EnableFooterWeatherShortcut();
                    app.Run(reader);
                    if (reader.FooterShortcutResult is { } readerShortcut) { nav = readerShortcut; continue; }
                }
            }
            else if (nav == LobbyNavigation.Gallery)
            {
                var gallery = services.GetRequiredService<Night.Ms.SshServer.Tui.Art.IArtGalleryProvider>();
                var screen = new GalleryScreen(app, services, user, gallery);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Map)
            {
                var rasterTiles = services.GetRequiredService<Night.Ms.SshServer.Tui.Map.IOsmTileFetcher>();
                var vectorTiles = services.GetRequiredService<Night.Ms.SshServer.Tui.Map.IVectorTileFetcher>();
                var logger = services.GetRequiredService<ILogger<MapScreen>>();
                var screen = new MapScreen(app, services, user, rasterTiles, vectorTiles, logger);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Weather)
            {
                using var scope = services.CreateScope();
                // WeatherScreen does not get EnableFooterWeatherShortcut — the user is already here.
                app.Run(new WeatherScreen(app, scope.ServiceProvider, user));
            }
            else if (nav == LobbyNavigation.Finance)
            {
                using var scope = services.CreateScope();
                var screen = new FinanceScreen(app, scope.ServiceProvider, user);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            else if (nav == LobbyNavigation.Alerts)
            {
                var alerts = lobbyScreen.LoadedAlerts;
                if (alerts is { Count: > 0 })
                {
                    var screen = new AlertsScreen(app, services, user, alerts);
                    screen.EnableFooterWeatherShortcut();
                    app.Run(screen);
                    if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
                }
            }
            else if (nav == LobbyNavigation.Sysop && user.IsSysop)
            {
                var screen = new AdminScreen(services, app, user);
                screen.EnableFooterWeatherShortcut();
                app.Run(screen);
                if (screen.FooterShortcutResult is { } shortcut) { nav = shortcut; continue; }
            }
            lobbyScreen = new LobbyScreen(app, services, user, justRegistered: false, art);
            lobbyScreen.EnableFooterWeatherShortcut();
            var nextLobbyResult = app.Run(lobbyScreen);
            nav = lobbyScreen.FooterShortcutResult ?? (LobbyNavigation?)nextLobbyResult;
        }
        }
        finally
        {
            wallCts.Cancel();
        }
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
            forumListScreen.EnableFooterWeatherShortcut();
            var forumResult = app.Run(forumListScreen);
            if (forumListScreen.FooterShortcutResult is { } s1) return s1;
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
                topicListScreen.EnableFooterWeatherShortcut();
                var listResult = (TopicListResult?)app.Run(topicListScreen);
                if (topicListScreen.FooterShortcutResult is { } s2) return s2;
                if (listResult == TopicListResult.Back) break;

                Topic? topic = null;
                if (listResult == TopicListResult.OpenTopic)
                {
                    topic = topicListScreen.SelectedTopic;
                }
                else if (listResult == TopicListResult.NewTopic)
                {
                    var newTopicScreen = new NewTopicScreen(app, services, user, forum);
                    newTopicScreen.EnableFooterWeatherShortcut();
                    var newTopicResult = app.Run(newTopicScreen);
                    if (newTopicScreen.FooterShortcutResult is { } s3) return s3;
                    topic = newTopicResult as Topic;
                }
                if (topic is null) continue; // back to topic list

                var threadScreen = new ThreadScreen(services, app, user, topic);
                threadScreen.EnableFooterWeatherShortcut();
                app.Run(threadScreen);
                if (threadScreen.FooterShortcutResult is { } s4) return s4;
            }
        }
    }

    private static async Task RunWallSubscriptionAsync(
        IServiceProvider services, IApplication app, CancellationToken ct)
    {
        var bus = services.GetRequiredService<IRealtimeBus>();
        await foreach (var payload in bus.SubscribeAsync(SystemTopics.Wall, ct))
        {
            WallBroadcastDto? dto;
            try { dto = JsonSerializer.Deserialize<WallBroadcastDto>(payload); }
            catch { continue; }
            if (dto is null) continue;

            app.Invoke(() =>
            {
                MessageBox.Query(
                    app,
                    title: "System Broadcast",
                    message: $"From sysop {dto.SysopHandle}:\n\n{dto.Message}",
                    "_OK");
            });
        }
    }

    private static Size GetPtySize(ITuiSession session)
    {
        var cols = (int)(session.Pty?.Cols ?? 80);
        var rows = (int)(session.Pty?.Rows ?? 24);
        return new Size(Math.Max(cols, 20), Math.Max(rows, 5));
    }
}
