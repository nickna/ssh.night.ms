using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Modal for changing (or setting) an SSH/web account password. Current password is required
// only if the user already has one set; on a fresh account (or for users who signed up via
// SSO and never set one), the field is hidden and the change is essentially "set initial
// password". Audit-logged as "password.changed" on success.
public sealed class PasswordChangeScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly User _user;
    private readonly AppDbContext _db;
    private readonly IPasswordHasher _hasher;
    private readonly NightMsOptions _options;

    public PasswordChangeScreen(IApplication app, IServiceProvider services, User user, AppDbContext db, IPasswordHasher hasher, NightMsOptions options)
        : base(app, services, user)
    {
        _app = app;
        _user = user;
        _db = db;
        _hasher = hasher;
        _options = options;
        Title = user.PasswordHash is null ? "set a password" : "change password";

        var blurb = new Label
        {
            X = 2,
            Y = 1,
            Text = user.PasswordHash is null
                ? "No password is set on your account yet — pick one.\nYou can use it to log in over SSH if your key isn't available."
                : "Change the password used for SSH and web login. Pick something strong.",
        };
        blurb.SetScheme(BbsTheme.Header_);

        int row = 4;

        TextField? currentField = null;
        if (user.PasswordHash is not null)
        {
            var currentLabel = new Label { X = 2, Y = row, Text = "Current password:" };
            currentLabel.SetScheme(BbsTheme.Hint);
            Add(currentLabel);
            currentField = new TextField { X = 2, Y = row + 1, Width = 36, Secret = true };
            currentField.SetScheme(BbsTheme.Input);
            Add(currentField);
            row += 3;
        }

        var newLabel = new Label { X = 2, Y = row, Text = $"New password (min {_options.PasswordHashing.MinPasswordLength} chars):" };
        newLabel.SetScheme(BbsTheme.Hint);
        Add(newLabel);
        var newField = new TextField { X = 2, Y = row + 1, Width = 36, Secret = true };
        newField.SetScheme(BbsTheme.Input);
        Add(newField);

        var confirmLabel = new Label { X = 2, Y = row + 3, Text = "Confirm new password:" };
        confirmLabel.SetScheme(BbsTheme.Hint);
        Add(confirmLabel);
        var confirmField = new TextField { X = 2, Y = row + 4, Width = 36, Secret = true };
        confirmField.SetScheme(BbsTheme.Input);
        Add(confirmField);

        var status = new Label { X = 2, Y = row + 6, Width = Dim.Fill(2), Height = 1 };
        status.SetScheme(BbsTheme.Warning);
        Add(status);

        var save = new Button { X = 2, Y = row + 8, Text = "_Save", IsDefault = true };
        var cancel = new Button { X = Pos.Right(save) + 2, Y = row + 8, Text = "_Cancel" };

        save.Accepting += async (_, e) =>
        {
            e.Handled = true;
            var current = currentField?.Text ?? string.Empty;
            var fresh = newField.Text ?? string.Empty;
            var confirm = confirmField.Text ?? string.Empty;

            if (_user.PasswordHash is not null && _user.PasswordAlgo is not null)
            {
                if (!_hasher.Verify(current, _user.PasswordHash, _user.PasswordAlgo))
                {
                    status.Text = "[!] Current password is incorrect.";
                    return;
                }
            }
            if (fresh.Length < _options.PasswordHashing.MinPasswordLength)
            {
                status.Text = $"[!] New password must be at least {_options.PasswordHashing.MinPasswordLength} characters.";
                return;
            }
            if (fresh != confirm)
            {
                status.Text = "[!] New passwords don't match.";
                return;
            }

            try
            {
                await SaveAsync(fresh);
                Result = "saved";
                _app.RequestStop();
            }
            catch (Exception ex)
            {
                status.Text = $"[!] save failed: {ex.Message}";
            }
        };

        cancel.Accepting += (_, e) => { e.Handled = true; _app.RequestStop(); };

        Add(save, cancel);
        (currentField as View ?? newField).SetFocus();
        InstallEscapeHandler();
    }

    private async Task SaveAsync(string password)
    {
        var hashed = _hasher.Hash(password);
        var tracked = await _db.Users.FirstAsync(u => u.Id == _user.Id);
        tracked.PasswordHash = hashed.Hash;
        tracked.PasswordAlgo = hashed.Algo;
        tracked.PasswordUpdatedAt = DateTimeOffset.UtcNow;
        _db.AuditLogs.Add(new AuditLog
        {
            ActorId = _user.Id,
            Action = "password.changed",
            TargetType = "user",
            TargetId = _user.Id,
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await _db.SaveChangesAsync();
        // Reflect on the in-memory copy so other screens see it immediately.
        _user.PasswordHash = hashed.Hash;
        _user.PasswordAlgo = hashed.Algo;
        _user.PasswordUpdatedAt = tracked.PasswordUpdatedAt;
    }
}
