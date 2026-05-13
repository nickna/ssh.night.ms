using System.Collections.ObjectModel;
using System.Net;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class ProfileEditScreen : BbsWindow
{
    private const int RightColumnX = 42;
    private const int TimeZoneListHeight = 6;

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly IPAddress? _clientIp;
    private readonly TextField _realName;
    private readonly TextField _location;
    private readonly TextView _bio;
    private readonly ListView _timeZoneList;
    private readonly string[] _timeZoneIds;
    private readonly OptionSelector<TemperatureUnit> _temperature;
    private readonly OptionSelector<ClockFormat> _clockFormat;
    private readonly OptionSelector<DateFormat> _dateFormat;
    private readonly BbsStatusLine _status;

    public ProfileEditScreen(IApplication app, IServiceProvider services, User user, IPAddress? clientIp)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _clientIp = clientIp;
        Title = $"profile — {user.Handle} — [Ctrl+S] save  [Esc] back to lobby";

        var blurb = new Label
        {
            X = 2,
            Y = 1,
            Text = $"Public profile for {user.Handle}. Other users see this via /finger.",
        };
        blurb.SetScheme(BbsTheme.Header_);
        Add(blurb);

        // ----- Left column: existing profile fields -----

        var realNameLabel = new Label { X = 2, Y = 3, Text = $"Real name (optional, ≤ {ProfileService.MaxRealNameLength}):" };
        realNameLabel.SetScheme(BbsTheme.Hint);
        Add(realNameLabel);
        _realName = new TextField { X = 2, Y = 4, Width = 36, Text = user.RealName ?? string.Empty };
        _realName.SetScheme(BbsTheme.Input);

        var locationLabel = new Label { X = 2, Y = 6, Text = $"Location (optional, ≤ {ProfileService.MaxLocationLength}):" };
        locationLabel.SetScheme(BbsTheme.Hint);
        Add(locationLabel);
        _location = new TextField { X = 2, Y = 7, Width = 36, Text = user.Location ?? string.Empty };
        _location.SetScheme(BbsTheme.Input);

        var bioLabel = new Label { X = 2, Y = 9, Text = $"Bio (optional, ≤ {ProfileService.MaxBioLength}):" };
        bioLabel.SetScheme(BbsTheme.Hint);
        Add(bioLabel);
        _bio = new TextView
        {
            X = 2,
            Y = 10,
            Width = 38,
            Height = 6,
            Text = user.Bio ?? string.Empty,
            WordWrap = true,
        };
        _bio.SetScheme(BbsTheme.Input);

        // ----- Right column: display preferences -----

        var prefsHeader = new Label { X = RightColumnX, Y = 3, Text = "── display preferences ──" };
        prefsHeader.SetScheme(BbsTheme.Header_);
        Add(prefsHeader);

        var tzLabel = new Label { X = RightColumnX, Y = 4, Text = "Time zone (type to jump):" };
        tzLabel.SetScheme(BbsTheme.Hint);
        Add(tzLabel);

        // Sort by base UTC offset, then by id. The label embeds the offset so it's visible
        // alongside the IANA id; ids stay in the same order so ListView's type-to-search hits
        // them by typing the leading characters of the id ("ameri…" → America/* zones).
        var zones = TimeZoneInfo.GetSystemTimeZones()
            .OrderBy(z => z.BaseUtcOffset)
            .ThenBy(z => z.Id, StringComparer.Ordinal)
            .ToArray();
        _timeZoneIds = zones.Select(z => z.Id).ToArray();
        var tzLabels = new ObservableCollection<string>(zones.Select(FormatZone));

        _timeZoneList = new ListView
        {
            X = RightColumnX,
            Y = 5,
            Width = 36,
            Height = TimeZoneListHeight,
        };
        _timeZoneList.SetSource(tzLabels);
        _timeZoneList.SetScheme(BbsTheme.Input);
        var currentIndex = Array.IndexOf(_timeZoneIds, user.TimeZoneId);
        if (currentIndex < 0) currentIndex = Array.IndexOf(_timeZoneIds, "UTC");
        if (currentIndex < 0) currentIndex = 0;
        _timeZoneList.SelectedItem = currentIndex;

        var temperatureLabel = new Label { X = RightColumnX, Y = 5 + TimeZoneListHeight + 1, Text = "Temperature:" };
        temperatureLabel.SetScheme(BbsTheme.Hint);
        Add(temperatureLabel);
        _temperature = new OptionSelector<TemperatureUnit>
        {
            X = RightColumnX,
            Y = 5 + TimeZoneListHeight + 2,
            Width = 36,
            Height = 1,
            Orientation = Orientation.Horizontal,
            Value = user.TemperatureUnit,
        };
        _temperature.Labels = ["Celsius", "Fahrenheit", "Both"];

        var clockLabel = new Label { X = RightColumnX, Y = 5 + TimeZoneListHeight + 4, Text = "Clock:" };
        clockLabel.SetScheme(BbsTheme.Hint);
        Add(clockLabel);
        _clockFormat = new OptionSelector<ClockFormat>
        {
            X = RightColumnX,
            Y = 5 + TimeZoneListHeight + 5,
            Width = 36,
            Height = 1,
            Orientation = Orientation.Horizontal,
            Value = user.ClockFormat,
        };
        _clockFormat.Labels = ["24-hour", "12-hour"];

        var dateLabel = new Label { X = RightColumnX, Y = 5 + TimeZoneListHeight + 7, Text = "Date format:" };
        dateLabel.SetScheme(BbsTheme.Hint);
        Add(dateLabel);
        _dateFormat = new OptionSelector<DateFormat>
        {
            X = RightColumnX,
            Y = 5 + TimeZoneListHeight + 8,
            Width = 36,
            Height = 1,
            Orientation = Orientation.Horizontal,
            Value = user.DateFormat,
        };
        // Labels carry today's date as a live preview so users see what each option produces.
        var today = DateTimeOffset.Now;
        _dateFormat.Labels = [
            today.ToString("yyyy-MM-dd"),
            today.ToString("M/d/yyyy"),
            today.ToString("d/M/yyyy"),
        ];

        // ----- Footer -----

        _status = new BbsStatusLine
        {
            X = 2,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(2),
            DefaultKind = BbsStatusLine.StatusKind.Status,
        };

        var save = new Button { X = 2, Y = Pos.AnchorEnd(4), Text = "_Save", IsDefault = true };
        save.Accepting += (_, e) => { e.Handled = true; SaveAsync().FireAndLog(_services, nameof(SaveAsync)); };

        var cancel = new Button { X = Pos.Right(save) + 2, Y = Pos.AnchorEnd(4), Text = "_Cancel" };
        cancel.Accepting += (_, e) => { e.Handled = true; _app.RequestStop(); };

        Add(_realName, _location, _bio, _timeZoneList, _temperature, _clockFormat, _dateFormat, _status, save, cancel);
        _realName.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                key.Handled = true;
                _app.RequestStop();
            }
            else if (key == Key.S.WithCtrl)
            {
                key.Handled = true;
                SaveAsync().FireAndLog(_services, nameof(SaveAsync));
            }
        };
    }

    private async Task SaveAsync()
    {
        try
        {
            var tzIndex = _timeZoneList.SelectedItem ?? 0;
            if (tzIndex < 0 || tzIndex >= _timeZoneIds.Length) tzIndex = 0;
            var update = new ProfileUpdate(
                RealName: _realName.Text,
                Location: _location.Text,
                Bio: _bio.Text,
                TimeZoneId: _timeZoneIds[tzIndex],
                TemperatureUnit: _temperature.Value ?? TemperatureUnit.Celsius,
                ClockFormat: _clockFormat.Value ?? ClockFormat.Hours24,
                DateFormat: _dateFormat.Value ?? DateFormat.Iso);

            var profile = _services.GetRequiredService<ProfileService>();
            var result = await profile.UpdateAsync(_user.Id, update, default);

            if (!result.Ok && result.Failure == ProfileUpdateFailure.LocationNotFound)
            {
                var applied = await TryApplyIpSuggestionAsync(profile, update);
                if (applied) return;
            }

            if (!result.Ok)
            {
                _app.Invoke(() => _status.SetWarning($"[!] {result.Error}"));
                return;
            }
            ReflectSavedUser(update);
            _user.LocationLatitude = result.LocationLatitude;
            _user.LocationLongitude = result.LocationLongitude;
            _user.LocationCanonical = result.LocationCanonical;
            _user.LocationSource = result.LocationSource;
            _app.Invoke(() => _status.SetSuccess("Saved."));
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.SetWarning($"[!] save failed: {ex.Message}"));
        }
    }

    // When geocoding rejects the typed location, fall back to the client's IP. If we can place
    // it on a map, prompt "use detected location: X?" — accepting writes through with the
    // resolved coords so the typed label persists alongside known-good lat/lon (source=IpGuess).
    private async Task<bool> TryApplyIpSuggestionAsync(ProfileService profile, ProfileUpdate originalUpdate)
    {
        if (_clientIp is null) return false;
        var ipProvider = _services.GetService<IIpGeolocationProvider>();
        if (ipProvider is null) return false;

        var suggestion = await ipProvider.LookupAsync(_clientIp);
        if (suggestion is null) return false;

        int? choice = -1;
        _app.Invoke(() =>
        {
            choice = Terminal.Gui.Views.MessageBox.Query(
                _app,
                title: "Location not found",
                message: $"Couldn't find '{originalUpdate.Location}'.\nUse detected location instead?\n\n  {suggestion.DisplayName}",
                "_Yes", "_No");
        });
        // MessageBox.Query is synchronous on the UI thread; choice is set before Invoke returns.
        if (choice != 0) return false;

        var ipUpdate = originalUpdate with
        {
            Location = suggestion.DisplayName,
            PreResolvedLocation = new PreResolvedLocation(
                suggestion.Latitude,
                suggestion.Longitude,
                suggestion.DisplayName,
                LocationSource.IpGuess),
        };
        var ipResult = await profile.UpdateAsync(_user.Id, ipUpdate, default);
        if (!ipResult.Ok)
        {
            _app.Invoke(() => _status.SetWarning($"[!] {ipResult.Error}"));
            return false;
        }
        _app.Invoke(() =>
        {
            _location.Text = suggestion.DisplayName;
        });
        ReflectSavedUser(ipUpdate);
        _user.LocationLatitude = suggestion.Latitude;
        _user.LocationLongitude = suggestion.Longitude;
        _user.LocationCanonical = suggestion.DisplayName;
        _user.LocationSource = LocationSource.IpGuess;
        _app.Invoke(() => _status.SetSuccess($"Saved with detected location: {suggestion.DisplayName}"));
        return true;
    }

    // Reflect the new values onto the in-memory User so the status bar (clock, weather unit)
    // and any open screens pick them up without a re-login.
    private void ReflectSavedUser(ProfileUpdate update)
    {
        _user.RealName = NullIfEmpty(update.RealName);
        _user.Location = NullIfEmpty(update.Location);
        _user.Bio = NullIfEmpty(update.Bio);
        _user.TimeZoneId = update.TimeZoneId;
        _user.TemperatureUnit = update.TemperatureUnit;
        _user.ClockFormat = update.ClockFormat;
        _user.DateFormat = update.DateFormat;
    }

    private static string FormatZone(TimeZoneInfo tz)
    {
        var offset = tz.BaseUtcOffset;
        var sign = offset < TimeSpan.Zero ? "-" : "+";
        return $"{tz.Id}  ({sign}{offset:hh\\:mm})";
    }

    private static string? NullIfEmpty(string? s) =>
        string.IsNullOrWhiteSpace(s) ? null : s.Trim();
}
