using System.Collections.ObjectModel;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class AdminScreen : Window
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _actor;
    private readonly ListView _userList;
    private readonly TextView _audit;
    private readonly Label _status;
    private List<User> _users = [];

    public AdminScreen(IServiceProvider services, IApplication app, User actor)
    {
        _services = services;
        _app = app;
        _actor = actor;
        Title = $"sysop console — {actor.Handle} — Tab to button row — [Esc] back";

        var leftHeader = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Percent(50),
            Text = "users:",
        };

        _userList = new ListView
        {
            X = 0,
            Y = Pos.Bottom(leftHeader),
            Width = Dim.Percent(50),
            Height = Dim.Fill(3),
        };

        var rightHeader = new Label
        {
            X = Pos.Right(_userList),
            Y = 0,
            Width = Dim.Fill(),
            Text = "audit log (recent 50):",
        };

        _audit = new TextView
        {
            X = Pos.Right(_userList),
            Y = Pos.Bottom(rightHeader),
            Width = Dim.Fill(),
            Height = Dim.Fill(3),
            ReadOnly = true,
            WordWrap = false,
            CanFocus = false,
        };

        var banButton = new Button { X = 0, Y = Pos.AnchorEnd(2), Text = "Toggle _Ban" };
        banButton.Accepting += (_, e) => { e.Handled = true; _ = ToggleBanAsync(); };

        var sysopButton = new Button { X = Pos.Right(banButton) + 1, Y = Pos.AnchorEnd(2), Text = "Toggle _Sysop" };
        sysopButton.Accepting += (_, e) => { e.Handled = true; _ = ToggleSysopAsync(); };

        var refreshButton = new Button { X = Pos.Right(sysopButton) + 1, Y = Pos.AnchorEnd(2), Text = "_Refresh" };
        refreshButton.Accepting += (_, e) => { e.Handled = true; _ = LoadAsync(); };

        _status = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(1),
            Width = Dim.Fill(),
            Text = "",
        };

        Add(leftHeader, _userList, rightHeader, _audit, banButton, sysopButton, refreshButton, _status);
        _userList.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                key.Handled = true;
                _app.RequestStop();
            }
        };

        _ = LoadAsync();
    }

    private async Task LoadAsync()
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            _users = await db.Users.OrderBy(u => u.Handle).Take(200).ToListAsync();
            var auditEntries = await db.AuditLogs
                .OrderByDescending(a => a.CreatedAt)
                .Take(50)
                .Include(a => a.Actor)
                .ToListAsync();

            _app.Invoke(() =>
            {
                _userList.SetSource<string>(new ObservableCollection<string>(_users.Select(FormatUser)));
                _audit.Text = string.Join("\n", auditEntries.Select(FormatAudit));
                _audit.SetNeedsDraw();
                _userList.SetNeedsDraw();
            });
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.Text = $"[!] load failed: {ex.Message}");
        }
    }

    private async Task ToggleBanAsync()
    {
        var target = SelectedUser();
        if (target is null) { _app.Invoke(() => _status.Text = "[!] Select a user first."); return; }
        if (target.Id == _actor.Id)
        {
            _app.Invoke(() => _status.Text = "[!] You can't ban yourself.");
            return;
        }

        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var user = await db.Users.FirstAsync(u => u.Id == target.Id);
            user.IsBanned = !user.IsBanned;
            db.AuditLogs.Add(new AuditLog
            {
                ActorId = _actor.Id,
                Action = user.IsBanned ? "user.ban" : "user.unban",
                TargetType = "user",
                TargetId = user.Id,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
            _app.Invoke(() => _status.Text = $"{(user.IsBanned ? "Banned" : "Unbanned")} {user.Handle}.");
            await LoadAsync();
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.Text = $"[!] toggle ban failed: {ex.Message}");
        }
    }

    private async Task ToggleSysopAsync()
    {
        var target = SelectedUser();
        if (target is null) { _app.Invoke(() => _status.Text = "[!] Select a user first."); return; }
        if (target.Id == _actor.Id)
        {
            _app.Invoke(() => _status.Text = "[!] You can't change your own sysop status.");
            return;
        }

        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var user = await db.Users.FirstAsync(u => u.Id == target.Id);
            user.IsSysop = !user.IsSysop;
            db.AuditLogs.Add(new AuditLog
            {
                ActorId = _actor.Id,
                Action = user.IsSysop ? "user.promote_sysop" : "user.demote_sysop",
                TargetType = "user",
                TargetId = user.Id,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
            _app.Invoke(() => _status.Text = $"{(user.IsSysop ? "Promoted" : "Demoted")} {user.Handle}.");
            await LoadAsync();
        }
        catch (Exception ex)
        {
            _app.Invoke(() => _status.Text = $"[!] toggle sysop failed: {ex.Message}");
        }
    }

    private User? SelectedUser()
    {
        var idx = _userList.SelectedItem ?? -1;
        if (idx < 0 || idx >= _users.Count) return null;
        return _users[idx];
    }

    private static string FormatUser(User u)
    {
        var flags = (u.IsSysop ? "S" : "-") + (u.IsBanned ? "B" : "-");
        var seen = u.LastSeenAt?.ToLocalTime().ToString("yyyy-MM-dd HH:mm") ?? "<never>";
        return $"{flags} {u.Handle,-20} {seen}";
    }

    private static string FormatAudit(AuditLog a)
    {
        var actor = a.Actor?.Handle ?? "<system>";
        var ts = a.CreatedAt.ToLocalTime().ToString("MM-dd HH:mm");
        return $"{ts} {actor,-12} {a.Action,-22} {a.TargetType}#{a.TargetId}";
    }
}
