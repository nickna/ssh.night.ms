using System.Collections.ObjectModel;
using System.Text;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class NewsScreen : BbsWindow
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _user;
    private readonly Label _weather;
    private readonly ListView _headlines;
    private readonly BbsStatusLine _status;
    private List<NewsHeadline> _items = [];

    public NewsScreen(IServiceProvider services, IApplication app, User user)
        : base(app, services, user)
    {
        _services = services;
        _app = app;
        _user = user;
        Title = "ssh.night.ms — news — [R] refresh — [Enter] read — [Esc] back to lobby";

        _weather = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "weather: (loading...)",
        };
        _weather.SetScheme(BbsTheme.Header_);

        var headlinesHeader = new Label
        {
            X = 0,
            Y = 2,
            Text = "headlines (Hacker News top stories):",
        };
        headlinesHeader.SetScheme(BbsTheme.Hint);

        _headlines = new ListView
        {
            X = 0,
            Y = 3,
            Width = Dim.Fill(),
            // Leaves 3 rows: status, footer (1 row each) + 1 spacer above status.
            Height = Dim.Fill(3),
        };

        _status = new BbsStatusLine
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
            DefaultKind = BbsStatusLine.StatusKind.Status,
        };
        _status.Set("loading...");

        _headlines.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                var idx = _headlines.SelectedItem ?? -1;
                if (idx >= 0 && idx < _items.Count)
                {
                    OpenStory(_items[idx]);
                    key.Handled = true;
                }
            }
        };

        Add(_weather, headlinesHeader, _headlines, _status);
        _headlines.SetFocus();

        InstallEscapeHandler();
        KeyDown += (_, key) =>
        {
            if (key.Matches(Key.R))
            {
                key.Handled = true;
                ReloadAsync().FireAndLog(_services, nameof(ReloadAsync));
            }
        };

        ReloadAsync().FireAndLog(_services, nameof(ReloadAsync));
    }

    private void OpenStory(NewsHeadline item)
    {
        if (Uri.TryCreate(item.Url, UriKind.Absolute, out var uri)
            && (uri.Scheme == Uri.UriSchemeHttp || uri.Scheme == Uri.UriSchemeHttps))
        {
            // Nested Application.Run — control returns here when ReaderScreen calls
            // RequestStop, with the news list's selection and scroll position intact.
            _app.Run(new ReaderScreen(_app, _services, _user, uri));
        }
        else
        {
            _app.Invoke(() => _status.Set("(no url for this story — Ask HN / Show HN are text-only)"));
        }
    }

    private async Task ReloadAsync()
    {
        _app.Invoke(() => _status.Set("loading..."));

        var weatherTask = LoadWeatherAsync();
        var newsTask = LoadNewsAsync();
        await Task.WhenAll(weatherTask, newsTask).ConfigureAwait(false);

        _app.Invoke(() => _status.Set($"updated {_user.FormatClockWithSeconds(DateTimeOffset.Now)} — Enter on a headline shows the URL down here"));
    }

    private async Task LoadWeatherAsync()
    {
        try
        {
            var provider = _services.GetRequiredService<IWeatherProvider>();
            var snap = await provider.GetCurrentAsync(
                latitude: _user.LocationLatitude,
                longitude: _user.LocationLongitude,
                label: _user.LocationCanonical ?? _user.Location);
            _app.Invoke(() =>
            {
                if (snap is null)
                {
                    _weather.Text = "weather: (unavailable)";
                    _weather.SetScheme(BbsTheme.Faint_);
                }
                else
                {
                    _weather.Text = FormatWeather(snap);
                    _weather.SetScheme(BbsTheme.Header_);
                }
            });
        }
        catch (Exception ex)
        {
            _app.Invoke(() =>
            {
                _weather.Text = $"weather: error — {ex.Message}";
                _weather.SetScheme(BbsTheme.Warning);
            });
        }
    }

    private async Task LoadNewsAsync()
    {
        try
        {
            var provider = _services.GetRequiredService<INewsProvider>();
            _items = (await provider.GetTopAsync(15)).ToList();
            _app.Invoke(() =>
            {
                _headlines.SetSource<string>(new ObservableCollection<string>(_items.Select(FormatHeadline)));
                _headlines.SetNeedsDraw();
            });
        }
        catch (Exception ex)
        {
            _app.Invoke(() =>
            {
                _items = [];
                _headlines.SetSource<string>(new ObservableCollection<string>([$"[!] news fetch failed: {ex.Message}"]));
            });
        }
    }

    private string FormatWeather(WeatherSnapshot s) =>
        $"weather: {s.LocationLabel}  {_user.FormatTemperature(s)}  {s.Conditions}";

    private static string FormatHeadline(NewsHeadline h)
    {
        var age = FormatHelpers.HumanizeAge(DateTimeOffset.UtcNow - h.PublishedAt);
        var score = h.Score is { } s ? $"[{s,4}]" : "[    ]";
        var byline = string.IsNullOrEmpty(h.Author) ? string.Empty : $" — {h.Author}";
        return $"{score} {h.Title}  ({age}{byline})";
    }
}
