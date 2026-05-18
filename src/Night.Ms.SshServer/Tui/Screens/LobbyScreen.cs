using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public enum LobbyNavigation { Chat, Boards, Profile, News, Browser, Gallery, Map, Weather, Alerts, Finance, Doors, Sysop, Logout }

public sealed class LobbyScreen : BbsWindow
{
    private readonly IApplication _app;

    public IReadOnlyList<WeatherAlert>? LoadedAlerts { get; private set; }

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

        var alertsBanner = new AlertsBannerView
        {
            X = 0,
            Y = contentTop + 2,
            Width = Dim.Fill(),
            Visible = false,
        };
        Add(alertsBanner);

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
            new LobbyEntry("Finance",  "finance", Key.K, LobbyNavigation.Finance),
            new LobbyEntry("Doors",    "doors",   Key.D, LobbyNavigation.Doors),
            new LobbyEntry("Sysop",    "sysop",   Key.S, LobbyNavigation.Sysop, Visible: user.IsSysop),
            new LobbyEntry("Logout",   "logout",  Key.L, LobbyNavigation.Logout),
        };

        var carouselEntries = entries
            .Where(e => e.Visible)
            .Select(e => new LobbyCarouselView<LobbyNavigation>.Entry(e.Label, e.Hotkey, e.Target, icons.Get(e.IconName)))
            .ToList();

        var carouselY = contentTop + 2;
        var carousel = new LobbyCarouselView<LobbyNavigation>(carouselEntries)
        {
            X = 0,
            Y = carouselY,
            Width = Dim.Fill(),
        };
        carousel.EntryActivated += (_, target) => Choose(target);
        Add(carousel);

        var sysopBadge = new Label
        {
            X = 2,
            Y = carouselY + LobbyCarouselView<LobbyNavigation>.RowHeight + 1,
            Text = user.IsSysop ? "[ sysop access granted — press S for the console ]" : string.Empty,
        };
        sysopBadge.SetScheme(BbsTheme.Success_);
        Add(sysopBadge);

        carousel.SetFocus();

        LoadAlertsAsync(app, services, user, alertsBanner, carousel, sysopBadge, carouselY)
            .FireAndLog(services, nameof(LoadAlertsAsync));

        app.AddTimeout(TimeSpan.FromSeconds(4), () =>
        {
            if (alertsBanner.AlertCount > 1)
                app.Invoke(() => alertsBanner.Advance());
            return true;
        });

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc) { key.Handled = true; Choose(LobbyNavigation.Logout); return; }
            if (key.Matches(Key.A))
            {
                key.Handled = true;
                if (LoadedAlerts is { Count: > 0 })
                    Choose(LobbyNavigation.Alerts);
                return;
            }

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

    private async Task LoadAlertsAsync(
        IApplication app, IServiceProvider services, User user,
        AlertsBannerView banner, LobbyCarouselView<LobbyNavigation> carousel, Label sysopBadge, int carouselY)
    {
        var alertProvider = services.GetRequiredService<IWeatherAlertProvider>();

        var locations = new List<(double Lat, double Lon)>();
        await using (var scope = services.CreateAsyncScope())
        {
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var saved = await db.UserSavedLocations
                .Where(s => s.UserId == user.Id)
                .OrderBy(s => s.SortOrder)
                .Take(9)
                .Select(s => new { s.Latitude, s.Longitude })
                .ToListAsync();
            foreach (var s in saved)
                locations.Add((s.Latitude, s.Longitude));
        }

        if (locations.Count == 0 && user.LocationLatitude.HasValue && user.LocationLongitude.HasValue)
            locations.Add((user.LocationLatitude.Value, user.LocationLongitude.Value));

        if (locations.Count == 0) return;

        var unique = locations
            .Select(l => (Math.Round(l.Lat, 2), Math.Round(l.Lon, 2)))
            .Distinct()
            .ToList();

        var tasks = unique.Select(c => alertProvider.GetActiveAlertsAsync(c.Item1, c.Item2));
        var results = await Task.WhenAll(tasks).ConfigureAwait(false);

        var all = results
            .SelectMany(r => r)
            .DistinctBy(a => a.Id)
            .OrderByDescending(a => a.Severity)
            .ToList();

        if (all.Count == 0) return;

        app.Invoke(() =>
        {
            LoadedAlerts = all;
            banner.SetAlerts(all);
            banner.Visible = true;
            var newCarouselY = carouselY + AlertsBannerView.BannerHeight;
            carousel.Y = newCarouselY;
            sysopBadge.Y = newCarouselY + LobbyCarouselView<LobbyNavigation>.RowHeight + 1;
        });
    }

    private void Choose(LobbyNavigation target)
    {
        Result = target;
        _app.RequestStop();
    }
}
