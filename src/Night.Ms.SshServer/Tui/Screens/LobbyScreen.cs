using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public enum LobbyNavigation { Chat, Boards, Profile, News, Browser, Gallery, Map, Weather, Sysop, Logout }

public sealed class LobbyScreen : BbsWindow
{
    private readonly IApplication _app;

    // The carousel entries are table-driven so a tenth destination is one row, and so the
    // hotkey loop + the per-card icon lookup share a single source of truth.
    private readonly record struct LobbyEntry(
        string Label,
        string IconName,
        Key Hotkey,
        LobbyNavigation Target,
        bool Visible = true);

    public LobbyScreen(IApplication app, IServiceProvider services, User user, bool justRegistered, ArtProvider art)
        : base(app, services, user)
    {
        _app = app;
        Title = $"ssh.night.ms — lobby — {user.Handle}";

        var icons = services.GetRequiredService<ILobbyIconProvider>();

        View artView;
        if (art.IsColor)
        {
            artView = new AnsiArtView { X = 0, Y = 0, Grid = art.Grid };
        }
        else
        {
            var label = new Label { X = 0, Y = 0, Text = art.Art };
            label.SetScheme(BbsTheme.Hint);
            artView = label;
        }

        var contentTop = art.LineCount + 1;

        var welcome = new Label
        {
            X = 2,
            Y = contentTop,
            Text = justRegistered
                ? $"Welcome aboard, {user.Handle}. Your key is bound to this account."
                : $"Welcome back, {user.Handle}.",
        };
        welcome.SetScheme(justRegistered ? BbsTheme.Success_ : BbsTheme.Header_);

        Add(artView, welcome);

        var entries = new[]
        {
            new LobbyEntry("Chat",     "chat",    Key.C, LobbyNavigation.Chat),
            new LobbyEntry("Boards",   "boards",  Key.B, LobbyNavigation.Boards),
            new LobbyEntry("Profile",  "profile", Key.P, LobbyNavigation.Profile),
            new LobbyEntry("News",     "news",    Key.N, LobbyNavigation.News),
            new LobbyEntry("Browser",  "browser", Key.W, LobbyNavigation.Browser),
            new LobbyEntry("Gallery",  "gallery", Key.G, LobbyNavigation.Gallery),
            new LobbyEntry("Map",      "map",     Key.M, LobbyNavigation.Map),
            new LobbyEntry("Weather",  "weather", Key.F, LobbyNavigation.Weather),
            new LobbyEntry("Sysop",    "sysop",   Key.S, LobbyNavigation.Sysop, Visible: user.IsSysop),
            new LobbyEntry("Logout",   "logout",  Key.L, LobbyNavigation.Logout),
        };

        var carouselEntries = entries
            .Where(e => e.Visible)
            .Select(e => new LobbyCarouselView.Entry(e.Label, e.Hotkey, e.Target, icons.Get(e.IconName)))
            .ToList();

        var carousel = new LobbyCarouselView(carouselEntries)
        {
            X = 0,
            Y = contentTop + 2,
            Width = Dim.Fill(),
        };
        carousel.EntryActivated += (_, target) => Choose(target);
        Add(carousel);

        var sysopBadge = new Label
        {
            X = 2,
            Y = contentTop + 2 + LobbyCarouselView.RowHeight + 1,
            Text = user.IsSysop ? "[ sysop access granted — press S for the console ]" : string.Empty,
        };
        sysopBadge.SetScheme(BbsTheme.Success_);
        Add(sysopBadge);

        carousel.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc) { key.Handled = true; Choose(LobbyNavigation.Logout); return; }

            foreach (var entry in entries)
            {
                if (!entry.Visible) continue;
                if (key == entry.Hotkey || key == entry.Hotkey.WithShift)
                {
                    key.Handled = true;
                    carousel.TrySelectByHotkey(key);
                    return;
                }
            }
        };
    }

    private void Choose(LobbyNavigation target)
    {
        Result = target;
        _app.RequestStop();
    }
}
