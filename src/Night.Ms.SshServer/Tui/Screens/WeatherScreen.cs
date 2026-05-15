using System.Globalization;
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

// Full-screen weather destination: animated ANSI banner + current conditions + a 24-hour
// hourly strip + a 7-day daily strip. Pulls from IWeatherProvider.GetForecastAsync; condition
// art comes from IWeatherAnimationProvider.GetFrames. A 500ms frame pump (via the same
// AddTimeout pattern BbsStatusBar uses) cycles frames as long as the screen is open.
//
// Travel: 'T' opens TravelLocationScreen, which returns a (lat, lon, canonical) pick that
// becomes the session-scoped active location. 'S' saves the active location as a favorite;
// F1..F9 quick-switch to the user's saved locations sorted by SortOrder.
public sealed class WeatherScreen : BbsWindow
{
    private const int BannerWidth = 40;
    private const int BannerHeight = 6;
    private const int BannerY = 2;
    private const int CurrentPanelX = BannerWidth + 2;
    // 4 cells per hour so values right-align inside a column with one space of breathing
    // room ("14h" → " 14h"). At 24 hours that's 96 cols — comfortable on a wide terminal.
    private const int HourlyStripCellWidth = 4;
    // Long enough to extend visually across a typical 80-cell terminal under the section
    // headers. Rendered in dim cyan to avoid competing with the header text.
    private const string SectionRule = " ────────────────────────────────────────────────────────────";
    private const int MaxFavorites = 9;
    private static readonly TimeSpan FrameInterval = TimeSpan.FromMilliseconds(500);

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly CancellationTokenSource _shutdown = new();

    private readonly Label _hintBar;
    private readonly Label _header;
    private readonly AnsiArtView _banner;
    private readonly Label _currentBlock;
    private readonly Label _hourlyHeader;
    private readonly Label _hourlyRow1; // hours
    private readonly Label _hourlyRow2; // temps
    private readonly Label _hourlyRow3; // precip%
    private readonly Label _dailyHeader;
    private readonly Label[] _dailyRows = new Label[7];
    private readonly BbsStatusLine _status;

    private object? _frameTimerToken;
    private IReadOnlyList<CellGrid> _frames = Array.Empty<CellGrid>();
    private int _frameIndex;
    private bool _disposed;

    private ActiveLocation _activeLocation;
    private List<UserSavedLocation> _favorites = new();

    public WeatherScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        Title = $"ssh.night.ms — weather — {user.Handle}";

        _activeLocation = ResolveInitialLocation(user);

        _hintBar = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "[T] travel   [S] save   [R] refresh   [F1-F9] favorites   [Esc] back",
        };
        _hintBar.SetScheme(BbsTheme.Hint);

        _header = new Label
        {
            X = 0,
            Y = 1,
            Width = Dim.Fill(),
            Text = string.Empty,
        };
        _header.SetScheme(BbsTheme.Header_);

        _banner = new AnsiArtView
        {
            X = 0,
            Y = BannerY,
        };

        _currentBlock = new Label
        {
            X = CurrentPanelX,
            Y = BannerY,
            Width = Dim.Fill(),
            Height = BannerHeight,
            Text = string.Empty,
        };

        _hourlyHeader = new Label
        {
            X = 0,
            Y = BannerY + BannerHeight + 1,
            Width = Dim.Fill(),
            Text = "Next 24 hours" + SectionRule,
        };
        _hourlyHeader.SetScheme(BbsTheme.Header_);

        _hourlyRow1 = new Label { X = 0, Y = BannerY + BannerHeight + 2, Width = Dim.Fill() };
        _hourlyRow2 = new Label { X = 0, Y = BannerY + BannerHeight + 3, Width = Dim.Fill() };
        _hourlyRow3 = new Label { X = 0, Y = BannerY + BannerHeight + 4, Width = Dim.Fill() };
        _hourlyRow3.SetScheme(BbsTheme.Faint_);

        _dailyHeader = new Label
        {
            X = 0,
            Y = BannerY + BannerHeight + 6,
            Width = Dim.Fill(),
            Text = "7-day forecast" + SectionRule,
        };
        _dailyHeader.SetScheme(BbsTheme.Header_);

        for (var i = 0; i < _dailyRows.Length; i++)
        {
            _dailyRows[i] = new Label
            {
                X = 0,
                Y = BannerY + BannerHeight + 7 + i,
                Width = Dim.Fill(),
                Text = string.Empty,
            };
        }

        _status = new BbsStatusLine
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
        };

        Add(_hintBar, _header, _banner, _currentBlock,
            _hourlyHeader, _hourlyRow1, _hourlyRow2, _hourlyRow3,
            _dailyHeader);
        foreach (var row in _dailyRows) Add(row);
        Add(_status);

        KeyDown += OnKeyDown;

        LoadFavoritesAsync().FireAndLog(_services, nameof(LoadFavoritesAsync));

        if (_activeLocation.IsEmpty)
        {
            ShowEmptyState();
        }
        else
        {
            RefreshAsync().FireAndLog(_services, nameof(RefreshAsync));
        }

        _frameTimerToken = _app.AddTimeout(FrameInterval, AdvanceFrame);
    }

    private void OnKeyDown(object? sender, Key key)
    {
        if (key == Key.Esc)
        {
            key.Handled = true;
            _app.RequestStop();
            return;
        }
        if (key == Key.T || key == Key.T.WithShift)
        {
            key.Handled = true;
            OpenTravel();
            return;
        }
        if (key == Key.S || key == Key.S.WithShift)
        {
            key.Handled = true;
            OpenSaveFavorite();
            return;
        }
        if (key == Key.R || key == Key.R.WithShift)
        {
            key.Handled = true;
            if (!_activeLocation.IsEmpty)
            {
                RefreshAsync().FireAndLog(_services, nameof(RefreshAsync));
            }
            return;
        }
        if (key == Key.M || key == Key.M.WithShift)
        {
            key.Handled = true;
            OpenFavoritesManager();
            return;
        }
        if (key == Key.A || key == Key.A.WithShift)
        {
            key.Handled = true;
            OpenAlertsForCurrentLocation();
            return;
        }

        var fnIndex = MapFunctionKey(key);
        if (fnIndex >= 0)
        {
            key.Handled = true;
            ActivateFavorite(fnIndex);
        }
    }

    private static int MapFunctionKey(Key key)
    {
        if (key == Key.F1) return 0;
        if (key == Key.F2) return 1;
        if (key == Key.F3) return 2;
        if (key == Key.F4) return 3;
        if (key == Key.F5) return 4;
        if (key == Key.F6) return 5;
        if (key == Key.F7) return 6;
        if (key == Key.F8) return 7;
        if (key == Key.F9) return 8;
        return -1;
    }

    private void OpenTravel()
    {
        var result = _app.Run(new TravelLocationScreen(_app, _services, _user)) as TravelLocationResult;
        if (result is null) return;

        _activeLocation = new ActiveLocation(result.Latitude, result.Longitude, result.Canonical);

        if (result.SaveAsFavorite)
        {
            SaveFavoriteAsync(result.Canonical, _activeLocation).FireAndLog(_services, nameof(SaveFavoriteAsync));
        }
        else
        {
            RefreshAsync().FireAndLog(_services, nameof(RefreshAsync));
        }
    }

    private void OpenSaveFavorite()
    {
        if (_activeLocation.IsEmpty)
        {
            _status.SetWarning("[!] No location active — press T to pick one first.");
            return;
        }

        var defaultLabel = _activeLocation.Label;
        var result = _app.Run(new SaveFavoritePromptScreen(_app, _services, _user, defaultLabel)) as string;
        if (string.IsNullOrWhiteSpace(result)) return;

        SaveFavoriteAsync(result.Trim(), _activeLocation).FireAndLog(_services, nameof(SaveFavoriteAsync));
    }

    private void ActivateFavorite(int index)
    {
        if (index < 0 || index >= _favorites.Count)
        {
            _status.Set($"No favorite at F{index + 1}.");
            return;
        }
        var fav = _favorites[index];
        _activeLocation = new ActiveLocation(fav.Latitude, fav.Longitude, fav.Canonical ?? fav.Label);
        RefreshAsync().FireAndLog(_services, nameof(RefreshAsync));
    }

    private void OpenFavoritesManager()
    {
        var pick = _app.Run(new FavoritesManagementScreen(_app, _services, _user)) as FavoritesManagementResult;

        // Always reload — the manager may have deleted/renamed/reordered rows even if the
        // user didn't pick one to activate.
        LoadFavoritesAsync().FireAndLog(_services, nameof(LoadFavoritesAsync));

        if (pick is null) return;

        var fav = pick.Selected;
        _activeLocation = new ActiveLocation(fav.Latitude, fav.Longitude, fav.Canonical ?? fav.Label);
        RefreshAsync().FireAndLog(_services, nameof(RefreshAsync));
    }

    private void OpenAlertsForCurrentLocation()
    {
        if (_activeLocation.IsEmpty)
        {
            _status.SetWarning("[!] No location active — press T to pick one first.");
            return;
        }
        _status.Set("loading alerts...");
        FetchAndShowAlertsAsync().FireAndLog(_services, "WeatherAlerts");
    }

    private async Task FetchAndShowAlertsAsync()
    {
        var alertProvider = _services.GetRequiredService<IWeatherAlertProvider>();
        var alerts = await alertProvider.GetActiveAlertsAsync(
            _activeLocation.Latitude, _activeLocation.Longitude, _shutdown.Token).ConfigureAwait(false);

        _app.Invoke(() =>
        {
            if (alerts.Count == 0)
            {
                _status.Set("No active alerts for this location.");
                return;
            }
            _app.Run(new AlertsScreen(_app, _services, _user, alerts));
            _status.Set(string.Empty);
        });
    }

    private async Task LoadFavoritesAsync()
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var favorites = await db.UserSavedLocations
                .Where(s => s.UserId == _user.Id)
                .OrderBy(s => s.SortOrder)
                .ThenBy(s => s.Id)
                .Take(MaxFavorites)
                .ToListAsync(_shutdown.Token);
            _app.Invoke(() =>
            {
                _favorites = favorites;
                UpdateHintBar();
            });
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] couldn't load favorites: {ex.Message}"));
        }
    }

    private async Task SaveFavoriteAsync(string label, ActiveLocation location)
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var existing = await db.UserSavedLocations
                .FirstOrDefaultAsync(s => s.UserId == _user.Id && s.Label == label, _shutdown.Token);
            if (existing is not null)
            {
                existing.Latitude = location.Latitude;
                existing.Longitude = location.Longitude;
                existing.Canonical = location.Label;
            }
            else
            {
                var count = await db.UserSavedLocations.CountAsync(s => s.UserId == _user.Id, _shutdown.Token);
                if (count >= MaxFavorites)
                {
                    _app.Invoke(() => _status.SetWarning($"[!] favorites full (max {MaxFavorites}) — remove one from the profile screen."));
                    return;
                }
                db.UserSavedLocations.Add(new UserSavedLocation
                {
                    UserId = _user.Id,
                    Label = label,
                    Latitude = location.Latitude,
                    Longitude = location.Longitude,
                    Canonical = location.Label,
                    SortOrder = count + 1,
                    CreatedAt = DateTimeOffset.UtcNow,
                });
            }
            await db.SaveChangesAsync(_shutdown.Token);

            _app.Invoke(() => _status.SetSuccess($"Saved '{label}'."));
            await LoadFavoritesAsync();
            _app.Invoke(() => RefreshAsync().FireAndLog(_services, nameof(RefreshAsync)));
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] save failed: {ex.Message}"));
        }
    }

    private async Task RefreshAsync()
    {
        if (_activeLocation.IsEmpty)
        {
            _app.Invoke(ShowEmptyState);
            return;
        }

        _app.Invoke(() => _status.Set("loading forecast..."));

        try
        {
            var provider = _services.GetRequiredService<IWeatherProvider>();
            var forecast = await provider.GetForecastAsync(
                latitude: _activeLocation.Latitude,
                longitude: _activeLocation.Longitude,
                label: _activeLocation.Label,
                cancellationToken: _shutdown.Token).ConfigureAwait(false);

            _app.Invoke(() =>
            {
                if (forecast is null)
                {
                    _header.Text = $"{_activeLocation.Label} — forecast unavailable";
                    _status.SetWarning("[!] couldn't reach the upstream weather service.");
                    _frames = Array.Empty<CellGrid>();
                    _banner.Grid = null;
                    return;
                }
                RenderForecast(forecast);
                _status.Set($"updated {_user.FormatClockWithSeconds(forecast.FetchedAt)}");
            });
        }
        catch (OperationCanceledException) { /* shutting down */ }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] error: {ex.Message}"));
        }
    }

    private void RenderForecast(WeatherForecast forecast)
    {
        var feelsLike = WeatherFormatter.Temperature(_user, forecast.Current.ApparentTemperatureCelsius, forecast.Current.ApparentTemperatureFahrenheit);
        var current = WeatherFormatter.Temperature(_user, forecast.Current.TemperatureCelsius, forecast.Current.TemperatureFahrenheit);
        _header.Text = $"{forecast.LocationLabel}  ·  {current}  feels {feelsLike}  ·  {forecast.Current.Conditions}";

        _currentBlock.Text = BuildCurrentBlock(forecast);

        RenderHourly(forecast.Hourly, forecast.TimeZone);
        RenderDaily(forecast.Daily);

        var condition = WeatherConditionMapper.Map(forecast.Current.WeatherCode, forecast.Current.IsDay);
        var animations = _services.GetRequiredService<IWeatherAnimationProvider>();
        _frames = animations.GetFrames(condition);
        _frameIndex = 0;
        _banner.Grid = _frames.Count > 0 ? _frames[0] : null;

        UpdateHintBar();
        SyncStatusBarSlot(forecast.LocationLabel);
    }

    // When the user is looking at a non-home location (via travel or a favorite), the
    // BbsStatusBar at the bottom still shows their home weather. Surface the discrepancy in
    // the status bar's middle slot so the user knows the bottom-line weather is *home*, not
    // *active*. Cleared when the active location matches home (or no home is set).
    private void SyncStatusBarSlot(string activeLabel)
    {
        if (IsHomeLocation(_activeLocation))
        {
            StatusBar.SetSlot(string.Empty);
        }
        else
        {
            StatusBar.SetSlot($"viewing: {activeLabel}");
        }
    }

    private bool IsHomeLocation(ActiveLocation loc)
    {
        if (_user.LocationLatitude is not { } homeLat || _user.LocationLongitude is not { } homeLon)
            return false;
        // Same precision as the forecast cache key (~1.1 km) — neighborhood-level match.
        return Math.Round(loc.Latitude, 2) == Math.Round(homeLat, 2)
            && Math.Round(loc.Longitude, 2) == Math.Round(homeLon, 2);
    }

    private string BuildCurrentBlock(WeatherForecast forecast)
    {
        var c = forecast.Current;
        // Six rows of details to fit beside the banner. Pre-format each line so the Label
        // wraps cleanly without us having to fight Terminal.Gui's wrap heuristics.
        var humidity = $"Humidity:    {c.RelativeHumidityPercent}%";
        var wind = $"Wind:        {WeatherFormatter.Wind(_user, c.WindSpeedKph, c.WindSpeedMph, c.WindDirectionDegrees)}";
        var precip = $"Precip:      {WeatherFormatter.Precipitation(_user, c.PrecipitationMm, c.PrecipitationInches)}";
        var sun = forecast.Daily.Count > 0
            ? $"Sun:         {_user.FormatClock(forecast.Daily[0].Sunrise)} / {_user.FormatClock(forecast.Daily[0].Sunset)}"
            : "Sun:         —";
        var uv = forecast.Daily.Count > 0
            ? $"UV index:    {forecast.Daily[0].UvIndexMax:F1}"
            : "UV index:    —";
        var tz = string.IsNullOrEmpty(forecast.TimeZone) ? string.Empty : $"Timezone:    {forecast.TimeZone}";

        return string.Join('\n', new[] { humidity, wind, precip, sun, uv, tz });
    }

    private void RenderHourly(IReadOnlyList<HourlyForecast> hourly, string? timeZone)
    {
        if (hourly.Count == 0)
        {
            _hourlyRow1.Text = "(no hourly data)";
            _hourlyRow2.Text = string.Empty;
            _hourlyRow3.Text = string.Empty;
            return;
        }

        var take = Math.Min(hourly.Count, 24);
        var hoursBuf = new System.Text.StringBuilder(take * HourlyStripCellWidth);
        var tempsBuf = new System.Text.StringBuilder(take * HourlyStripCellWidth);
        var precipBuf = new System.Text.StringBuilder(take * HourlyStripCellWidth);
        for (var i = 0; i < take; i++)
        {
            var h = hourly[i];
            // Open-Meteo gave us a DateTimeOffset already pinned to the location's offset.
            // Display its local hour directly (don't re-convert through the user's tz — they
            // came here to see the destination's weather, not their own clock).
            var hour = h.Time.Hour;
            hoursBuf.Append(FixedCell($"{hour:D2}h"));
            tempsBuf.Append(FixedCell(WeatherFormatter.ShortTemperature(_user, h.TemperatureCelsius, h.TemperatureFahrenheit)));
            precipBuf.Append(FixedCell($"{h.PrecipitationProbabilityPercent,2}%"));
        }
        _hourlyRow1.Text = hoursBuf.ToString();
        _hourlyRow2.Text = tempsBuf.ToString();
        _hourlyRow3.Text = precipBuf.ToString();
    }

    private static string FixedCell(string content)
    {
        if (content.Length >= HourlyStripCellWidth) return content[..HourlyStripCellWidth];
        // Right-aligned with one leading space of separation between adjacent cells.
        return content.PadLeft(HourlyStripCellWidth);
    }

    private void RenderDaily(IReadOnlyList<DailyForecast> daily)
    {
        // Use fixed-width composite format so the slash, precip column, and condition
        // column line up identically on every row regardless of value widths.
        for (var i = 0; i < _dailyRows.Length; i++)
        {
            if (i >= daily.Count)
            {
                _dailyRows[i].Text = string.Empty;
                continue;
            }
            var d = daily[i];
            var day = d.Date.ToString("ddd MMM d", CultureInfo.InvariantCulture);
            var hi = WeatherFormatter.ShortTemperature(_user, d.TemperatureMaxCelsius, d.TemperatureMaxFahrenheit);
            var lo = WeatherFormatter.ShortTemperature(_user, d.TemperatureMinCelsius, d.TemperatureMinFahrenheit);
            // Column widths: day=12, hi=4, " / ", lo=4 (left-padded), gap, precip=4 ("100%"), gap, condition.
            _dailyRows[i].Text =
                $"{day,-12}  {hi,4} / {lo,-4}    {d.PrecipitationProbabilityMaxPercent,3}%   {d.Conditions}";
        }
    }

    private void ShowEmptyState()
    {
        _header.Text = "No location set — press T to pick a city.";
        _currentBlock.Text = "Add a location on the Profile screen, or use Travel here for a one-off lookup.";
        _hourlyRow1.Text = string.Empty;
        _hourlyRow2.Text = string.Empty;
        _hourlyRow3.Text = string.Empty;
        foreach (var row in _dailyRows) row.Text = string.Empty;
        _status.Set("Press T to set a location.");
        _frames = Array.Empty<CellGrid>();
        _banner.Grid = null;
    }

    private void UpdateHintBar()
    {
        if (_favorites.Count == 0)
        {
            _hintBar.Text = "[T] travel  [S] save  [M] manage  [A] alerts  [R] refresh  [Esc] back";
            return;
        }
        var names = string.Join(" ", _favorites
            .Take(MaxFavorites)
            .Select((f, i) => $"F{i + 1}:{Truncate(f.Label, 12)}"));
        _hintBar.Text = $"[T] travel  [S] save  [M] manage  [A] alerts  [R] refresh  [Esc] back   {names}";
    }

    private static string Truncate(string s, int max) => s.Length <= max ? s : s[..max];

    private bool AdvanceFrame()
    {
        if (_disposed) return false;
        if (_frames.Count == 0) return true;
        _app.Invoke(() =>
        {
            if (_disposed || _frames.Count == 0) return;
            _frameIndex = (_frameIndex + 1) % _frames.Count;
            _banner.Grid = _frames[_frameIndex];
        });
        return true;
    }

    private static ActiveLocation ResolveInitialLocation(User user)
    {
        if (user.LocationLatitude is { } lat && user.LocationLongitude is { } lon)
        {
            var label = user.LocationCanonical ?? user.Location ?? "your location";
            return new ActiveLocation(lat, lon, label);
        }
        return ActiveLocation.Empty;
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing && !_disposed)
        {
            _disposed = true;
            try { _shutdown.Cancel(); } catch { /* ignore */ }
            if (_frameTimerToken is not null)
            {
                try { _app.RemoveTimeout(_frameTimerToken); } catch { /* ignore */ }
                _frameTimerToken = null;
            }
            _shutdown.Dispose();
        }
        base.Dispose(disposing);
    }

    private readonly record struct ActiveLocation(double Latitude, double Longitude, string Label)
    {
        public static ActiveLocation Empty => new(double.NaN, double.NaN, string.Empty);
        public bool IsEmpty => double.IsNaN(Latitude) || double.IsNaN(Longitude);
    }
}

// Format helpers tied to the user's TemperatureUnit / display prefs. Could grow into a
// public extension method if a second screen needs them, but for now they live alongside
// the only consumer.
internal static class WeatherFormatter
{
    public static string Temperature(User? user, double celsius, double fahrenheit) =>
        (user?.TemperatureUnit ?? TemperatureUnit.Celsius) switch
        {
            TemperatureUnit.Fahrenheit => $"{fahrenheit:F0}°F",
            TemperatureUnit.Both => $"{celsius:F0}°C/{fahrenheit:F0}°F",
            _ => $"{celsius:F0}°C",
        };

    // Compact form used inside the hourly strip cells (3 cols each) — strips the °C/°F so
    // it fits without truncating numbers. Unit is implied by the screen header.
    public static string ShortTemperature(User? user, double celsius, double fahrenheit)
    {
        var unit = user?.TemperatureUnit ?? TemperatureUnit.Celsius;
        var value = unit == TemperatureUnit.Fahrenheit ? fahrenheit : celsius;
        return $"{value:F0}°";
    }

    public static string Wind(User? user, double kph, double mph, int directionDeg)
    {
        var bearing = CompassBearing(directionDeg);
        return (user?.TemperatureUnit ?? TemperatureUnit.Celsius) == TemperatureUnit.Fahrenheit
            ? $"{mph:F0} mph {bearing}"
            : $"{kph:F0} km/h {bearing}";
    }

    public static string Precipitation(User? user, double mm, double inches) =>
        (user?.TemperatureUnit ?? TemperatureUnit.Celsius) == TemperatureUnit.Fahrenheit
            ? $"{inches:F2} in"
            : $"{mm:F1} mm";

    private static string CompassBearing(int deg)
    {
        // Eight points cover the common renderings without overshooting the 38-col panel.
        deg = ((deg % 360) + 360) % 360;
        var sector = ((deg + 22) / 45) % 8;
        return sector switch
        {
            0 => "N",
            1 => "NE",
            2 => "E",
            3 => "SE",
            4 => "S",
            5 => "SW",
            6 => "W",
            7 => "NW",
            _ => string.Empty,
        };
    }
}
