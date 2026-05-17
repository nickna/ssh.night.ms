using System.Collections.ObjectModel;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Lists the user's SSH keys and lets them remove individual ones. Add-by-paste is web-only
// because pasting an OpenSSH public key block into a TG TextField is awkward over a PTY
// (multi-line, often gets line-wrapped); the lobby blurb steers them to the web page for
// this. Refuses removal when it would leave the account with zero ways to log in (no other
// SSH key AND no password set).
public sealed class KeysManagementScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly User _user;
    private readonly AppDbContext _db;
    private readonly ListView _list;
    private List<IdentityCredential> _keys = new();
    private readonly Label _status;

    public KeysManagementScreen(IApplication app, IServiceProvider services, User user, AppDbContext db)
        : base(app, services, user)
    {
        _app = app;
        _user = user;
        _db = db;
        Title = "ssh keys";

        var blurb = new Label
        {
            X = 2,
            Y = 1,
            Text =
                "SSH keys registered to your account. Connecting with any of them logs you\n" +
                "in without a password. To add a key, paste its public form into your web\n" +
                "profile — pasting public keys over an SSH terminal is fiddly.",
        };
        blurb.SetScheme(BbsTheme.Header_);
        Add(blurb);

        _list = new ListView
        {
            X = 2,
            Y = 5,
            Width = Dim.Fill(2),
            Height = Dim.Fill(7),
        };
        _list.SetScheme(BbsTheme.Input);
        Add(_list);

        _status = new Label { X = 2, Y = Pos.AnchorEnd(4), Width = Dim.Fill(2), Height = 1 };
        _status.SetScheme(BbsTheme.Warning);
        Add(_status);

        var remove = new Button { X = 2, Y = Pos.AnchorEnd(2), Text = "_Remove selected" };
        var close = new Button { X = Pos.Right(remove) + 2, Y = Pos.AnchorEnd(2), Text = "_Close", IsDefault = true };
        remove.Accepting += async (_, e) =>
        {
            e.Handled = true;
            await RemoveSelectedAsync();
        };
        close.Accepting += (_, e) => { e.Handled = true; _app.RequestStop(); };
        Add(remove, close);

        ReloadAsync().FireAndLog(services, nameof(ReloadAsync));
        _list.SetFocus();
        InstallEscapeHandler();
    }

    private async Task ReloadAsync()
    {
        _keys = await _db.IdentityCredentials
            .Where(c => c.UserId == _user.Id && c.Provider == CredentialProvider.Ssh)
            .OrderBy(c => c.CreatedAt)
            .ToListAsync();
        var rows = new ObservableCollection<string>(_keys.Select(FormatRow));
        _app.Invoke(() =>
        {
            _list.SetSource(rows);
            if (rows.Count > 0) _list.SelectedItem = 0;
        });
    }

    private async Task RemoveSelectedAsync()
    {
        var idx = _list.SelectedItem ?? -1;
        if (idx < 0 || idx >= _keys.Count) return;
        var credential = _keys[idx];

        // Last-credential guard: a user must always have ≥1 way back in. Other SSH keys
        // or a set password both qualify.
        var otherKeys = _keys.Count - 1;
        var hasPassword = await _db.Users
            .AnyAsync(u => u.Id == _user.Id && u.PasswordHash != null);
        if (otherKeys + (hasPassword ? 1 : 0) < 1)
        {
            _app.Invoke(() => _status.Text = "[!] Set a password first — you'd lock yourself out.");
            return;
        }

        _db.IdentityCredentials.Remove(credential);
        _db.AuditLogs.Add(new AuditLog
        {
            ActorId = _user.Id,
            Action = "identity.unlinked",
            TargetType = "identity_credential",
            TargetId = credential.Id,
            CreatedAt = DateTimeOffset.UtcNow,
            Details = System.Text.Json.JsonSerializer.SerializeToDocument(new
            {
                provider = "Ssh",
                fingerprint = credential.Subject,
                via = "tui",
            }),
        });
        await _db.SaveChangesAsync();
        _app.Invoke(() => _status.Text = "Removed.");
        await ReloadAsync();
    }

    private static string FormatRow(IdentityCredential c)
    {
        var label = string.IsNullOrEmpty(c.Label) ? "(no label)" : c.Label;
        var used = c.LastUsedAt is { } u ? u.ToString("yyyy-MM-dd") : "never";
        return $"  {label,-30}  {c.Subject}   added {c.CreatedAt:yyyy-MM-dd}   last used {used}";
    }
}
