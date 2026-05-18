using System.Text;
using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Diagnostics;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class AdminScreen : BbsWindow
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _actor;
    private readonly TextView _userPane;
    private readonly TextView _audit;
    private readonly Label _metrics;
    private readonly BbsStatusLine _status;
    private readonly TextField _command;
    private readonly SystemMetricsCollector _metricsCollector;

    public AdminScreen(IServiceProvider services, IApplication app, User actor)
        : base(app, services, actor)
    {
        _services = services;
        _app = app;
        _actor = actor;
        Title = $"sysop console — {actor.Handle} — type 'help' + Enter — [Esc] back to lobby";

        var leftHeader = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Percent(50),
            Text = "users (S=sysop, B=banned):",
        };
        leftHeader.SetScheme(BbsTheme.Header_);

        _userPane = new TextView
        {
            X = 0,
            Y = Pos.Bottom(leftHeader),
            Width = Dim.Percent(50),
            Height = Dim.Fill(4),
            ReadOnly = true,
            WordWrap = false,
            CanFocus = false,
        };

        var rightHeader = new Label
        {
            X = Pos.Right(_userPane),
            Y = 0,
            Width = Dim.Fill(),
            Text = "audit log (recent 50):",
        };
        rightHeader.SetScheme(BbsTheme.Header_);

        _audit = new TextView
        {
            X = Pos.Right(_userPane),
            Y = Pos.Bottom(rightHeader),
            Width = Dim.Fill(),
            Height = Dim.Fill(4),
            ReadOnly = true,
            WordWrap = false,
            CanFocus = false,
        };

        _metrics = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(4),
            Width = Dim.Fill(),
            Text = string.Empty,
        };
        _metrics.SetScheme(BbsTheme.Status);

        _status = new BbsStatusLine
        {
            X = 0,
            Y = Pos.AnchorEnd(3),
            Width = Dim.Fill(),
            Text = "Commands: ban | unban | sysop | unsysop | clear-passwordless <handle> | wall <msg> | refresh",
        };

        _command = new TextField
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
        };
        _command.SetScheme(BbsTheme.Input);
        _command.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                key.Handled = true;
                var line = (_command.Text ?? string.Empty).Trim();
                if (line.Length > 0)
                {
                    _command.Text = string.Empty;
                    ExecuteAsync(line).FireAndLog(_services, nameof(ExecuteAsync));
                }
            }
        };

        Add(leftHeader, _userPane, rightHeader, _audit, _metrics, _status, _command);
        _command.SetFocus();

        InstallEscapeHandler();

        _metricsCollector = _services.GetRequiredService<SystemMetricsCollector>();
        UpdateMetricsLabel(_metricsCollector.Sample());
        Task.Run(() => MetricsLoopAsync(Shutdown))
            .FireAndLog(_services, nameof(MetricsLoopAsync));

        LoadAsync().FireAndLog(_services, nameof(LoadAsync));
    }

    private async Task MetricsLoopAsync(CancellationToken ct)
    {
        try
        {
            while (!ct.IsCancellationRequested)
            {
                await Task.Delay(TimeSpan.FromSeconds(2), ct);
                var snap = _metricsCollector.Sample();
                _app.Invoke(() => UpdateMetricsLabel(snap));
            }
        }
        catch (OperationCanceledException) { /* expected on screen exit */ }
    }

    private void UpdateMetricsLabel(SystemMetricsSnapshot snap)
    {
        _metrics.Text = snap.FormatCompact();
        _metrics.SetNeedsDraw();
    }

    private async Task ExecuteAsync(string line)
    {
        var parts = line.Split(' ', 2, StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        var verb = parts[0].ToLowerInvariant();
        var target = parts.Length > 1 ? parts[1] : null;

        try
        {
            switch (verb)
            {
                case "help":
                case "?":
                    SetStatus("ban | unban | sysop | unsysop | clear-passwordless <handle> | wall <msg> | refresh");
                    return;
                case "refresh":
                    await LoadAsync();
                    SetStatus("Refreshed.");
                    return;
                case "ban":
                    await TogglePropertyAsync(target, isBan: true, expectedAfter: true);
                    return;
                case "unban":
                    await TogglePropertyAsync(target, isBan: true, expectedAfter: false);
                    return;
                case "sysop":
                    await TogglePropertyAsync(target, isBan: false, expectedAfter: true);
                    return;
                case "unsysop":
                    await TogglePropertyAsync(target, isBan: false, expectedAfter: false);
                    return;
                case "clear-passwordless":
                    await ClearPasswordlessAsync(target);
                    return;
                case "wall":
                    await WallAsync(target);
                    return;
                default:
                    SetStatus($"[!] Unknown command: '{verb}'. Type 'help'.");
                    return;
            }
        }
        catch (Exception ex)
        {
            SetStatus($"[!] {verb} failed: {ex.Message}");
        }
    }

    private async Task TogglePropertyAsync(string? handle, bool isBan, bool expectedAfter)
    {
        if (string.IsNullOrEmpty(handle))
        {
            SetStatus($"[!] {(isBan ? "ban/unban" : "sysop/unsysop")} requires a handle.");
            return;
        }

        await using var scope = _services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var target = await db.Users.FirstOrDefaultAsync(u => u.Handle == handle);
        if (target is null)
        {
            SetStatus($"[!] No such user: '{handle}'.");
            return;
        }
        if (target.Id == _actor.Id)
        {
            SetStatus("[!] You can't change your own status.");
            return;
        }

        string action;
        if (isBan)
        {
            target.IsBanned = expectedAfter;
            action = expectedAfter ? "user.ban" : "user.unban";
        }
        else
        {
            target.IsSysop = expectedAfter;
            action = expectedAfter ? "user.promote_sysop" : "user.demote_sysop";
        }

        db.AuditLogs.Add(new AuditLog
        {
            ActorId = _actor.Id,
            Action = action,
            TargetType = "user",
            TargetId = target.Id,
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await db.SaveChangesAsync();
        SetStatus($"{action} {target.Handle}");
        await LoadAsync();
    }

    // Recovery hatch for users who locked themselves out by enabling RequireSshKey and then
    // losing every key on file. Idempotent — clearing an already-off flag still logs an audit
    // row so the action is visible in the trail.
    private async Task ClearPasswordlessAsync(string? handle)
    {
        if (string.IsNullOrEmpty(handle))
        {
            SetStatus("[!] clear-passwordless requires a handle.");
            return;
        }

        await using var scope = _services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        var target = await db.Users.FirstOrDefaultAsync(u => u.Handle == handle);
        if (target is null)
        {
            SetStatus($"[!] No such user: '{handle}'.");
            return;
        }
        if (!target.RequireSshKey)
        {
            SetStatus($"[!] {target.Handle} doesn't have passwordless mode enabled.");
            return;
        }

        target.RequireSshKey = false;
        db.AuditLogs.Add(new AuditLog
        {
            ActorId = _actor.Id,
            Action = "user.passwordless.reset_by_sysop",
            TargetType = "user",
            TargetId = target.Id,
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await db.SaveChangesAsync();
        SetStatus($"clear-passwordless {target.Handle}");
        await LoadAsync();
    }

    private async Task WallAsync(string? message)
    {
        if (string.IsNullOrWhiteSpace(message))
        {
            SetStatus("[!] wall requires a message. Usage: wall <message>");
            return;
        }
        if (message.Length > 500)
        {
            SetStatus("[!] Wall message too long (max 500 chars).");
            return;
        }

        int? choice = -1;
        _app.Invoke(() =>
        {
            choice = MessageBox.Query(
                _app,
                title: "Confirm broadcast",
                message: $"Send to ALL connected sessions?\n\n\"{message}\"",
                "_Send", "_Cancel");
        });
        if (choice != 0)
        {
            SetStatus("Wall broadcast cancelled.");
            return;
        }

        var bus = _services.GetRequiredService<IRealtimeBus>();
        var dto = new WallBroadcastDto(_actor.Handle, message, DateTimeOffset.UtcNow);
        var bytes = JsonSerializer.SerializeToUtf8Bytes(dto);
        await bus.PublishAsync(SystemTopics.Wall, bytes);

        await using var scope = _services.CreateAsyncScope();
        var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
        db.AuditLogs.Add(new AuditLog
        {
            ActorId = _actor.Id,
            Action = "wall.broadcast",
            TargetType = "system",
            TargetId = null,
            Details = JsonDocument.Parse(JsonSerializer.Serialize(new { message })),
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await db.SaveChangesAsync();
        SetStatus("Wall broadcast sent.");
        await LoadAsync();
    }

    private async Task LoadAsync()
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var users = await db.Users.OrderBy(u => u.Handle).Take(200).ToListAsync();
            var auditEntries = await db.AuditLogs
                .OrderByDescending(a => a.CreatedAt)
                .Take(50)
                .Include(a => a.Actor)
                .ToListAsync();

            var userText = string.Join("\n", users.Select(u => FormatUser(u, _actor)));
            var auditText = string.Join("\n", auditEntries.Select(a => FormatAudit(a, _actor)));

            _app.Invoke(() =>
            {
                _userPane.Text = userText;
                _audit.Text = auditText;
                _userPane.SetNeedsDraw();
                _audit.SetNeedsDraw();
            });
        }
        catch (Exception ex)
        {
            SetStatus($"[!] load failed: {ex.Message}");
        }
    }

    private void SetStatus(string text) => _app.Invoke(() => _status.Set(text));

    private static string FormatUser(User u, User viewer)
    {
        var flags = (u.IsSysop ? "S" : "-") + (u.IsBanned ? "B" : "-");
        var seen = u.LastSeenAt is { } ls ? viewer.FormatDateTime(ls) : "<never>";
        return $"{flags} {u.Handle,-20} {seen}";
    }

    private static string FormatAudit(AuditLog a, User viewer)
    {
        var actor = a.Actor?.Handle ?? "<system>";
        var ts = viewer.FormatDateTime(a.CreatedAt);
        return $"{ts} {actor,-12} {a.Action,-22} {a.TargetType}#{a.TargetId}";
    }
}
