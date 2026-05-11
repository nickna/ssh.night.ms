using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class ProfileEditScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly TextField _realName;
    private readonly TextField _location;
    private readonly TextView _bio;
    private readonly Label _status;

    public ProfileEditScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services)
    {
        _app = app;
        _services = services;
        _user = user;
        Title = $"profile — {user.Handle} — [Ctrl+S] save  [Esc] back to lobby";

        var blurb = new Label
        {
            X = 2,
            Y = 1,
            Text = $"Public profile for {user.Handle}. Other users see this via /finger.",
        };
        blurb.SetScheme(BbsTheme.Header_);
        Add(blurb);

        var realNameLabel = new Label { X = 2, Y = 3, Text = $"Real name (optional, ≤ {ProfileService.MaxRealNameLength} chars):" };
        realNameLabel.SetScheme(BbsTheme.Hint);
        Add(realNameLabel);
        _realName = new TextField { X = 2, Y = 4, Width = ProfileService.MaxRealNameLength, Text = user.RealName ?? string.Empty };
        _realName.SetScheme(BbsTheme.Input);

        var locationLabel = new Label { X = 2, Y = 6, Text = $"Location (optional, ≤ {ProfileService.MaxLocationLength} chars):" };
        locationLabel.SetScheme(BbsTheme.Hint);
        Add(locationLabel);
        _location = new TextField { X = 2, Y = 7, Width = ProfileService.MaxLocationLength, Text = user.Location ?? string.Empty };
        _location.SetScheme(BbsTheme.Input);

        var bioLabel = new Label { X = 2, Y = 9, Text = $"Bio (optional, ≤ {ProfileService.MaxBioLength} chars):" };
        bioLabel.SetScheme(BbsTheme.Hint);
        Add(bioLabel);
        _bio = new TextView
        {
            X = 2,
            Y = 10,
            Width = Dim.Fill(2),
            Height = 6,
            Text = user.Bio ?? string.Empty,
            WordWrap = true,
        };
        _bio.SetScheme(BbsTheme.Input);

        _status = new Label { X = 2, Y = Pos.AnchorEnd(2), Width = Dim.Fill(2) };
        _status.SetScheme(BbsTheme.Status);

        var save = new Button { X = 2, Y = Pos.AnchorEnd(4), Text = "_Save", IsDefault = true };
        save.Accepting += (_, e) => { e.Handled = true; _ = SaveAsync(); };

        var cancel = new Button { X = Pos.Right(save) + 2, Y = Pos.AnchorEnd(4), Text = "_Cancel" };
        cancel.Accepting += (_, e) => { e.Handled = true; _app.RequestStop(); };

        Add(_realName, _location, _bio, _status, save, cancel);
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
                _ = SaveAsync();
            }
        };
    }

    private async Task SaveAsync()
    {
        try
        {
            var profile = _services.GetRequiredService<ProfileService>();
            var result = await profile.UpdateAsync(_user.Id, _realName.Text, _location.Text, _bio.Text, default);
            if (!result.Ok)
            {
                _app.Invoke(() =>
                {
                    _status.Text = $"[!] {result.Error}";
                    _status.SetScheme(BbsTheme.Warning);
                });
                return;
            }
            // Reflect the new values onto the in-memory User so the lobby chrome stays in sync.
            _user.RealName = NullIfEmpty(_realName.Text);
            _user.Location = NullIfEmpty(_location.Text);
            _user.Bio = NullIfEmpty(_bio.Text);
            _app.Invoke(() =>
            {
                _status.Text = "Saved.";
                _status.SetScheme(BbsTheme.Success_);
            });
        }
        catch (Exception ex)
        {
            _app.Invoke(() =>
            {
                _status.Text = $"[!] save failed: {ex.Message}";
                _status.SetScheme(BbsTheme.Warning);
            });
        }
    }

    private static string? NullIfEmpty(string? s) =>
        string.IsNullOrWhiteSpace(s) ? null : s.Trim();
}
