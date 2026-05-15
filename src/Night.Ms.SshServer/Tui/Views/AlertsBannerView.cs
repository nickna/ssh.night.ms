using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.Drawing;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Views;

internal sealed class AlertsBannerView : View
{
    public const int BannerHeight = 2;

    private readonly Label _alertLine;
    private readonly Label _hintLine;
    private IReadOnlyList<WeatherAlert> _alerts = [];
    private int _currentIndex;

    public int AlertCount => _alerts.Count;
    public IReadOnlyList<WeatherAlert> Alerts => _alerts;

    public AlertsBannerView()
    {
        CanFocus = false;
        Height = BannerHeight;

        _alertLine = new Label { X = 2, Y = 0, Width = Dim.Fill(2) };
        _hintLine = new Label { X = 2, Y = 1, Width = Dim.Fill(2) };
        _hintLine.SetScheme(BbsTheme.Faint_);

        Add(_alertLine, _hintLine);
    }

    public void SetAlerts(IReadOnlyList<WeatherAlert> alerts)
    {
        _alerts = alerts;
        _currentIndex = 0;
        Render();
    }

    public void Advance()
    {
        if (_alerts.Count <= 1) return;
        _currentIndex = (_currentIndex + 1) % _alerts.Count;
        Render();
    }

    private void Render()
    {
        if (_alerts.Count == 0) return;

        var alert = _alerts[_currentIndex];
        _alertLine.Text = $"[!] {alert.Event} — {alert.AreaDescription}";
        _alertLine.SetScheme(SchemeForSeverity(alert.Severity));

        _hintLine.Text = _alerts.Count > 1
            ? $"[A] view details  ({_currentIndex + 1}/{_alerts.Count})"
            : "[A] view details";
    }

    private static Scheme SchemeForSeverity(AlertSeverity severity) => severity switch
    {
        AlertSeverity.Extreme or AlertSeverity.Severe => BbsTheme.Warning,
        AlertSeverity.Moderate => BbsTheme.Header_,
        _ => BbsTheme.Hint,
    };
}
