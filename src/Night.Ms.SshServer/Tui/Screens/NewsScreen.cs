using System.Collections.ObjectModel;
using System.Text;
using Night.Ms.SshServer.Providers;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class NewsScreen : Window
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly Label _weather;
    private readonly ListView _headlines;
    private readonly Label _status;
    private List<NewsHeadline> _items = [];

    public NewsScreen(IServiceProvider services, IApplication app)
    {
        _services = services;
        _app = app;
        Title = "ssh.night.ms — news — [R] refresh — [Enter] copy url — [Esc] back to lobby";

        _weather = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "weather: (loading...)",
        };

        var headlinesHeader = new Label
        {
            X = 0,
            Y = 2,
            Text = "headlines (Hacker News top stories):",
        };

        _headlines = new ListView
        {
            X = 0,
            Y = 3,
            Width = Dim.Fill(),
            Height = Dim.Fill(2),
        };

        _status = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(1),
            Width = Dim.Fill(),
            Text = "loading...",
        };

        _headlines.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                var idx = _headlines.SelectedItem ?? -1;
                if (idx >= 0 && idx < _items.Count)
                {
                    var item = _items[idx];
                    _app.Invoke(() => _status.Text = item.Url ?? "(no url for this story)");
                    key.Handled = true;
                }
            }
        };

        Add(_weather, headlinesHeader, _headlines, _status);
        _headlines.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                key.Handled = true;
                _app.RequestStop();
            }
            else if (key == Key.R || key == Key.R.WithShift)
            {
                key.Handled = true;
                _ = ReloadAsync();
            }
        };

        _ = ReloadAsync();
    }

    private async Task ReloadAsync()
    {
        _app.Invoke(() => _status.Text = "loading...");

        var weatherTask = LoadWeatherAsync();
        var newsTask = LoadNewsAsync();
        await Task.WhenAll(weatherTask, newsTask).ConfigureAwait(false);

        _app.Invoke(() => _status.Text = $"updated {DateTime.Now:HH:mm:ss} — Enter on a headline shows the URL down here");
    }

    private async Task LoadWeatherAsync()
    {
        try
        {
            var provider = _services.GetRequiredService<IWeatherProvider>();
            var snap = await provider.GetCurrentAsync();
            _app.Invoke(() => _weather.Text = snap is null
                ? "weather: (unavailable)"
                : FormatWeather(snap));
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _weather.Text = $"weather: error — {ex.Message}");
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

    private static string FormatWeather(WeatherSnapshot s) =>
        $"weather: {s.LocationLabel}  {s.TemperatureCelsius:F1}°C / {s.TemperatureFahrenheit:F0}°F  {s.Conditions}";

    private static string FormatHeadline(NewsHeadline h)
    {
        var age = HumanizeAge(DateTimeOffset.UtcNow - h.PublishedAt);
        var score = h.Score is { } s ? $"[{s,4}]" : "[    ]";
        var byline = string.IsNullOrEmpty(h.Author) ? string.Empty : $" — {h.Author}";
        return $"{score} {h.Title}  ({age}{byline})";
    }

    private static string HumanizeAge(TimeSpan age)
    {
        if (age.TotalMinutes < 60) return $"{(int)Math.Max(1, age.TotalMinutes)}m ago";
        if (age.TotalHours < 24) return $"{(int)age.TotalHours}h ago";
        return $"{(int)age.TotalDays}d ago";
    }
}
