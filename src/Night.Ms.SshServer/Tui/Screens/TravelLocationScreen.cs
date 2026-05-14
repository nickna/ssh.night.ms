using System.Collections.ObjectModel;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Returned by TravelLocationScreen when the user picks a match. Null Result means the user
// cancelled. SaveAsFavorite tells WeatherScreen to persist the chosen pick as a favorite
// rather than just using it for the session.
public sealed record TravelLocationResult(
    double Latitude,
    double Longitude,
    string Canonical,
    bool SaveAsFavorite);

// Modal city-search screen. User types a name, hits Enter, picks from up to five matches.
// Backed by IGeocodingProvider (Open-Meteo's keyless geocoding API). On select, returns a
// TravelLocationResult; on Esc, returns null.
public sealed class TravelLocationScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly TextField _query;
    private readonly ListView _results;
    private readonly CheckBox _saveAsFavorite;
    private readonly BbsStatusLine _status;
    private List<GeocodingMatch> _matches = new();

    public TravelLocationScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        Title = "ssh.night.ms — travel — [Enter] search/select — [Esc] cancel";

        var prompt = new Label
        {
            X = 2,
            Y = 1,
            Text = "Type a city, region, or country:",
        };
        prompt.SetScheme(BbsTheme.Hint);

        _query = new TextField
        {
            X = 2,
            Y = 3,
            Width = Dim.Fill(2),
        };
        _query.SetScheme(BbsTheme.Input);
        _query.Accepting += (_, e) =>
        {
            e.Handled = true;
            SearchAsync().FireAndLog(_services, nameof(SearchAsync));
        };

        var resultsHeader = new Label
        {
            X = 2,
            Y = 5,
            Text = "Matches (Enter to select):",
        };
        resultsHeader.SetScheme(BbsTheme.Hint);

        _results = new ListView
        {
            X = 2,
            Y = 6,
            Width = Dim.Fill(2),
            Height = Dim.Fill(5),
        };
        _results.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                key.Handled = true;
                PickCurrent();
            }
        };

        _saveAsFavorite = new CheckBox
        {
            X = 2,
            Y = Pos.AnchorEnd(3),
            Text = "Save as favorite",
        };

        _status = new BbsStatusLine
        {
            X = 2,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(2),
        };
        _status.Set("Type a query and press Enter to search.");

        Add(prompt, _query, resultsHeader, _results, _saveAsFavorite, _status);
        _query.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                key.Handled = true;
                Result = null;
                _app.RequestStop();
            }
        };
    }

    private async Task SearchAsync()
    {
        var query = (_query.Text ?? string.Empty).Trim();
        if (query.Length == 0)
        {
            _status.SetWarning("[!] enter at least one character.");
            return;
        }

        _app.Invoke(() => _status.Set("searching..."));
        try
        {
            var provider = _services.GetRequiredService<IGeocodingProvider>();
            var matches = await provider.SearchAsync(query).ConfigureAwait(false);
            _app.Invoke(() =>
            {
                if (matches is null)
                {
                    _matches = new();
                    _results.SetSource<string>(new ObservableCollection<string>());
                    _status.SetWarning("[!] geocoding service unreachable.");
                    return;
                }
                _matches = matches.ToList();
                if (_matches.Count == 0)
                {
                    _results.SetSource<string>(new ObservableCollection<string>([$"(no matches for \"{query}\")"]));
                    _status.Set("Try a different spelling, or include the country.");
                    return;
                }
                _results.SetSource<string>(new ObservableCollection<string>(_matches.Select(FormatMatch)));
                _status.Set("Use ↑/↓ then Enter to pick a city.");
                _results.SetFocus();
            });
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] {ex.Message}"));
        }
    }

    private void PickCurrent()
    {
        var idx = _results.SelectedItem ?? -1;
        if (idx < 0 || idx >= _matches.Count) return;
        var m = _matches[idx];
        Result = new TravelLocationResult(
            Latitude: m.Latitude,
            Longitude: m.Longitude,
            Canonical: m.CanonicalName,
            SaveAsFavorite: _saveAsFavorite.Value == CheckState.Checked);
        _app.RequestStop();
    }

    private static string FormatMatch(GeocodingMatch m) =>
        $"{m.CanonicalName}  ({m.Latitude:F2}, {m.Longitude:F2})";
}
