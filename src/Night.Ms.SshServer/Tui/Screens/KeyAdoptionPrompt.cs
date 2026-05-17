using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Shown after a user successfully logs in via password while having offered an SSH key
// that isn't registered to their account. Three outcomes:
//   Adopt      → write a new IdentityCredential, future SSH logins skip the password
//   NotNow     → no-op, prompt reappears on next login (transient dismissal)
//   NeverAsk   → write a dismissal to Redis (TTL ~90d), prompt won't re-appear for this
//                fingerprint until the TTL elapses
public sealed class KeyAdoptionPrompt : BbsWindow
{
    private readonly IApplication _app;
    private readonly User _user;
    private readonly string _fingerprint;
    private readonly string _algorithm;
    private readonly byte[] _blob;
    private readonly AppDbContext _db;
    private readonly IDismissedKeyStore _dismissals;

    public KeyAdoptionPrompt(
        IApplication app,
        IServiceProvider services,
        User user,
        string fingerprint,
        string algorithm,
        byte[] blob,
        AppDbContext db,
        IDismissedKeyStore dismissals)
        : base(app, services, user)
    {
        _app = app;
        _user = user;
        _fingerprint = fingerprint;
        _algorithm = algorithm;
        _blob = blob;
        _db = db;
        _dismissals = dismissals;
        Title = "ssh.night.ms — new key detected";

        var explainer = new Label
        {
            X = 2,
            Y = 1,
            Text =
                "You logged in with an SSH key your account doesn't know about yet.\n" +
                "Adopting it means future logins from this machine skip the password.\n" +
                "You can revoke it later from your profile.",
        };
        explainer.SetScheme(BbsTheme.Header_);

        var keyInfo = new Label
        {
            X = 2,
            Y = 5,
            Text = $"  algorithm  {algorithm}\n  fingerprint  {fingerprint}",
        };
        keyInfo.SetScheme(BbsTheme.Faint_);

        var labelPrompt = new Label
        {
            X = 2,
            Y = 8,
            Text = "Optional label (so you can recognise it later):",
        };
        labelPrompt.SetScheme(BbsTheme.Hint);

        var labelField = new TextField
        {
            X = 2,
            Y = 9,
            Width = 40,
            Text = $"adopted {DateTimeOffset.Now:yyyy-MM-dd}",
        };
        labelField.SetScheme(BbsTheme.Input);

        var status = new Label { X = 2, Y = 11, Width = Dim.Fill(2), Height = 1 };
        status.SetScheme(BbsTheme.Warning);

        var adopt = new Button { X = 2, Y = 13, Text = "_Add to my account", IsDefault = true };
        var notNow = new Button { X = Pos.Right(adopt) + 2, Y = 13, Text = "_Not now" };
        var never = new Button { X = Pos.Right(notNow) + 2, Y = 13, Text = "Ne_ver for this key" };

        adopt.Accepting += async (_, e) =>
        {
            e.Handled = true;
            try
            {
                await AdoptKeyAsync((labelField.Text ?? string.Empty).Trim());
                _app.RequestStop();
            }
            catch (DbUpdateException)
            {
                status.Text = "[!] This key is already attached to another account.";
            }
            catch (Exception ex)
            {
                status.Text = $"[!] Failed to adopt key: {ex.Message}";
            }
        };

        notNow.Accepting += (_, e) =>
        {
            e.Handled = true;
            _app.RequestStop();
        };

        never.Accepting += async (_, e) =>
        {
            e.Handled = true;
            await _dismissals.DismissAsync(_user.Id, _fingerprint, default);
            _app.RequestStop();
        };

        Add(explainer, keyInfo, labelPrompt, labelField, status, adopt, notNow, never);
        adopt.SetFocus();
        InstallEscapeHandler();
    }

    private async Task AdoptKeyAsync(string label)
    {
        // Guard: the unique (Provider, Subject) index would catch this too, but a clean
        // pre-check lets us surface a friendlier error than a DbUpdateException stack.
        var existing = await _db.IdentityCredentials.AsNoTracking()
            .FirstOrDefaultAsync(c => c.Provider == CredentialProvider.Ssh && c.Subject == _fingerprint);
        if (existing is not null)
        {
            throw new InvalidOperationException(existing.UserId == _user.Id
                ? "Key is already on your account."
                : "Key is registered to another account.");
        }

        var metadata = System.Text.Json.JsonSerializer.Serialize(new
        {
            algorithm = _algorithm,
            blob_b64 = Convert.ToBase64String(_blob),
        });
        _db.IdentityCredentials.Add(new IdentityCredential
        {
            UserId = _user.Id,
            Provider = CredentialProvider.Ssh,
            Subject = _fingerprint,
            Metadata = metadata,
            Label = string.IsNullOrEmpty(label) ? null : label,
            CreatedAt = DateTimeOffset.UtcNow,
            LastUsedAt = DateTimeOffset.UtcNow,
        });
        await _db.SaveChangesAsync();
    }
}
