using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.StatusBar;

// Persistent footer row attached to every BbsWindow. Renders, left-to-right:
//
//   HH:mm:ss │ ☼ Location  Temp / Temp  Conditions │ <reserved slot> │ ssh.night.ms
//
// Clock refreshes every second; weather refreshes every five minutes (the underlying
// IWeatherProvider caches for ten, so most ticks return the cached value).
public sealed class BbsStatusBar : View
{
    private const string Brand = "ssh.night.ms";

    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly IWeatherProvider? _weather;
    private readonly User? _user;
    private readonly Label _clock;
    private readonly Label _weatherLabel;
    private readonly Label _slot;
    private readonly Label _brand;
    private readonly CancellationTokenSource _shutdown = new();
    private object? _clockTimerToken;
    private object? _weatherTimerToken;
    private bool _disposed;

    public BbsStatusBar(IApplication app, IServiceProvider services, User? user)
    {
        _services = services;
        _app = app;
        _weather = services.GetService<IWeatherProvider>();
        _user = user;

        Height = 1;
        Width = Dim.Fill();
        SetScheme(BbsTheme.Status);

        // Width 11 fits both "HH:mm:ss" (24h, 8 cols) and "h:mm:ss tt" (12h, up to 11 cols).
        _clock = new Label { X = 0, Y = 0, Width = 11, Text = string.Empty };
        _clock.SetScheme(BbsTheme.Header_);

        var sep1 = new Label { X = Pos.Right(_clock) + 1, Y = 0, Width = 1, Text = "│" };

        _weatherLabel = new Label
        {
            X = Pos.Right(sep1) + 1,
            Y = 0,
            Width = 40,
            Text = "weather: …",
        };

        var sep2 = new Label { X = Pos.Right(_weatherLabel) + 1, Y = 0, Width = 1, Text = "│" };

        _slot = new Label
        {
            X = Pos.Right(sep2) + 1,
            Y = 0,
            Width = Dim.Fill(Brand.Length + 3),
            Text = string.Empty,
        };

        _brand = new Label
        {
            X = Pos.AnchorEnd(Brand.Length),
            Y = 0,
            Width = Brand.Length,
            Text = Brand,
        };
        _brand.SetScheme(BbsTheme.Hint);

        Add(_clock, sep1, _weatherLabel, sep2, _slot, _brand);

        Tick();
        _clockTimerToken = _app.AddTimeout(TimeSpan.FromSeconds(1), Tick);
        // First weather refresh fires immediately; subsequent ones every 5 minutes.
        RefreshWeatherAsync().FireAndLog(_services, nameof(RefreshWeatherAsync));
        _weatherTimerToken = _app.AddTimeout(TimeSpan.FromMinutes(5), () =>
        {
            RefreshWeatherAsync().FireAndLog(_services, nameof(RefreshWeatherAsync));
            return true;
        });
    }

    // Updates the middle "reserved slot" — call from screens or background services to surface
    // ambient counters (users online, unread DMs, etc.). Empty string clears it.
    public void SetSlot(string text) => _app.Invoke(() =>
    {
        _slot.Text = text ?? string.Empty;
        _slot.SetNeedsDraw();
    });

    private bool Tick()
    {
        if (_disposed) return false;
        _app.Invoke(() =>
        {
            _clock.Text = _user.FormatClockWithSeconds(DateTimeOffset.Now);
            _clock.SetNeedsDraw();
        });
        return true;
    }

    private async Task RefreshWeatherAsync()
    {
        if (_weather is null)
        {
            _app.Invoke(() => _weatherLabel.Text = "weather: (offline)");
            return;
        }

        try
        {
            var snap = await _weather.GetCurrentAsync(
                latitude: _user?.LocationLatitude,
                longitude: _user?.LocationLongitude,
                label: _user?.LocationCanonical ?? _user?.Location,
                cancellationToken: _shutdown.Token).ConfigureAwait(false);
            _app.Invoke(() =>
            {
                _weatherLabel.Text = snap is null
                    ? "weather: (unavailable)"
                    : $"{snap.LocationLabel} {_user.FormatTemperature(snap)} {snap.Conditions}";
                _weatherLabel.SetNeedsDraw();
            });
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _weatherLabel.Text = $"weather: error ({ex.GetType().Name})");
        }
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing && !_disposed)
        {
            _disposed = true;
            try { _shutdown.Cancel(); } catch { /* ignore */ }
            if (_clockTimerToken is not null) { try { _app.RemoveTimeout(_clockTimerToken); } catch { /* ignore */ } }
            if (_weatherTimerToken is not null) { try { _app.RemoveTimeout(_weatherTimerToken); } catch { /* ignore */ } }
            _shutdown.Dispose();
        }
        base.Dispose(disposing);
    }
}
