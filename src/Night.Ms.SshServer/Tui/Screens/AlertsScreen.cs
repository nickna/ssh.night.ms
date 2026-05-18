using System.Collections.ObjectModel;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class AlertsScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IReadOnlyList<WeatherAlert> _alerts;
    private readonly ListView _listView;
    private readonly Label _detailView;
    private int _lastShownIndex = -1;

    public AlertsScreen(IApplication app, IServiceProvider services, User user, IReadOnlyList<WeatherAlert> alerts)
        : base(app, services, user)
    {
        _app = app;
        _alerts = alerts;
        Title = $"Weather Alerts ({alerts.Count})";

        var hintBar = new Label
        {
            X = 2, Y = 0, Width = Dim.Fill(2),
            Text = "[↑/↓] navigate   [Enter] details   [Esc] back",
        };
        hintBar.SetScheme(BbsTheme.Hint);
        Add(hintBar);

        var listHeight = Math.Min(alerts.Count, 8);
        _listView = new ListView
        {
            X = 1, Y = 2,
            Width = Dim.Fill(1),
            Height = listHeight,
        };
        _listView.SetSource<string>(new ObservableCollection<string>(alerts.Select(FormatListItem)));

        _listView.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                key.Handled = true;
                ShowDetail();
            }
            else if (key == Key.CursorUp || key == Key.CursorDown || key == Key.K || key == Key.J)
            {
                app.Invoke(ShowDetailIfChanged);
            }
        };
        Add(_listView);

        _detailView = new Label
        {
            X = 2, Y = 2 + listHeight + 1,
            Width = Dim.Fill(2),
            Height = Dim.Fill(2),
            Text = string.Empty,
        };
        _detailView.SetScheme(BbsTheme.Faint_);
        Add(_detailView);

        _listView.SetFocus();
        ShowDetail();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc || key.Matches(Key.Q))
            {
                key.Handled = true;
                _app.RequestStop();
            }
        };
    }

    private void ShowDetailIfChanged()
    {
        var idx = _listView.SelectedItem ?? 0;
        if (idx != _lastShownIndex) ShowDetail();
    }

    private void ShowDetail()
    {
        var idx = _listView.SelectedItem ?? 0;
        if (idx < 0 || idx >= _alerts.Count) return;
        _lastShownIndex = idx;
        var alert = _alerts[idx];

        var expires = alert.Expires.ToLocalTime().ToString("g");
        _detailView.Text = $"{alert.Headline}\n\nArea: {alert.AreaDescription}\nSeverity: {alert.Severity}   Expires: {expires}\n\n{alert.Description}";
        _detailView.SetScheme(SchemeForSeverity(alert.Severity));
    }

    private static string FormatListItem(WeatherAlert alert)
    {
        var sev = alert.Severity switch
        {
            AlertSeverity.Extreme => "!!!",
            AlertSeverity.Severe => "!! ",
            AlertSeverity.Moderate => "!  ",
            _ => "   ",
        };
        var expires = alert.Expires.ToLocalTime().ToString("t");
        return $"{sev} {alert.Event} — {alert.AreaDescription}  (until {expires})";
    }

    private static Scheme SchemeForSeverity(AlertSeverity severity) => severity switch
    {
        AlertSeverity.Extreme or AlertSeverity.Severe => BbsTheme.Warning,
        AlertSeverity.Moderate => BbsTheme.Header_,
        _ => BbsTheme.Hint,
    };
}
