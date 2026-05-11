using System.Text;
using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

public sealed class ChatScreen : Window
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _user;
    private readonly Channel _channel;
    private readonly TextView _log;
    private readonly TextField _input;
    private readonly Label _status;
    private readonly CancellationTokenSource _shutdown = new();
    private Task? _subscriber;

    public ChatScreen(IServiceProvider services, IApplication app, User user, Channel channel)
    {
        _services = services;
        _app = app;
        _user = user;
        _channel = channel;
        Title = $"#{channel.Name} — {user.Handle} — [Esc] back to lobby";

        _log = new TextView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(3),
            ReadOnly = true,
            WordWrap = true,
        };

        _status = new Label
        {
            X = 0,
            Y = Pos.Bottom(_log),
            Width = Dim.Fill(),
            Text = $"channel #{channel.Name}  topic: {channel.Topic ?? "(none)"}",
        };

        _input = new TextField
        {
            X = 0,
            Y = Pos.Bottom(_status),
            Width = Dim.Fill(),
        };

        _input.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                key.Handled = true;
                var text = (_input.Text ?? string.Empty).Trim();
                if (text.Length > 0)
                {
                    _input.Text = string.Empty;
                    _ = SendAsync(text);
                }
            }
        };

        Add(_log, _status, _input);
        _input.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                key.Handled = true;
                _shutdown.Cancel();
                _app.RequestStop();
            }
        };

        _ = LoadHistoryAndSubscribeAsync();
    }

    private async Task LoadHistoryAndSubscribeAsync()
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var history = await db.ChatMessages
                .Where(m => m.ChannelId == _channel.Id)
                .OrderByDescending(m => m.CreatedAt)
                .Take(20)
                .Include(m => m.User)
                .ToListAsync(_shutdown.Token);

            history.Reverse();
            foreach (var msg in history)
            {
                AppendOnUiThread(FormatMessage(msg.CreatedAt, msg.User?.Handle ?? "?", msg.Body));
            }

            _subscriber = Task.Run(() => RunSubscribeAsync(_shutdown.Token));
        }
        catch (OperationCanceledException) { /* expected on screen close */ }
        catch (Exception ex)
        {
            AppendOnUiThread($"[!] history load failed: {ex.Message}\n");
        }
    }

    private async Task RunSubscribeAsync(CancellationToken ct)
    {
        var bus = _services.GetRequiredService<IRealtimeBus>();
        await foreach (var payload in bus.SubscribeAsync(ChatTopics.Channel(_channel.Id), ct))
        {
            ChatMessageDto? msg;
            try
            {
                msg = JsonSerializer.Deserialize<ChatMessageDto>(payload);
            }
            catch
            {
                continue;
            }
            if (msg is null) continue;

            AppendOnUiThread(FormatMessage(msg.CreatedAt, msg.Handle, msg.Body));
        }
    }

    private async Task SendAsync(string body)
    {
        try
        {
            var now = DateTimeOffset.UtcNow;
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var msg = new ChatMessage
            {
                ChannelId = _channel.Id,
                UserId = _user.Id,
                Body = body,
                CreatedAt = now,
            };
            db.ChatMessages.Add(msg);
            await db.SaveChangesAsync(_shutdown.Token);

            var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
            var dto = new ChatMessageDto(msg.Id, _channel.Id, _user.Id, _user.Handle, body, now);
            await bus.PublishAsync(ChatTopics.Channel(_channel.Id), JsonSerializer.SerializeToUtf8Bytes(dto), _shutdown.Token);
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendOnUiThread($"[!] send failed: {ex.Message}\n");
        }
    }

    private void AppendOnUiThread(string text)
    {
        _app.Invoke(() =>
        {
            var current = _log.Text ?? string.Empty;
            _log.Text = current + text;
            // Pin the cursor at the bottom of the buffer so new lines are visible.
            _log.MoveEnd();
            _log.SetNeedsDraw();
        });
    }

    private static string FormatMessage(DateTimeOffset at, string handle, string body) =>
        $"[{at.ToLocalTime():HH:mm}] {handle}: {body}\n";

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            try { _shutdown.Cancel(); } catch { /* ignore */ }
            _shutdown.Dispose();
        }
        base.Dispose(disposing);
    }
}
