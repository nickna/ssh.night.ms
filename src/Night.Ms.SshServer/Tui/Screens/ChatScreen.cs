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
    private readonly TextView _log;
    private readonly TextField _input;
    private readonly Label _status;
    private readonly CancellationTokenSource _shutdown = new();
    private Channel _currentChannel;
    private CancellationTokenSource _channelCts = new();
    private Task? _subscriber;

    public ChatScreen(IServiceProvider services, IApplication app, User user, Channel initialChannel)
    {
        _services = services;
        _app = app;
        _user = user;
        _currentChannel = initialChannel;

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
                    _ = HandleInputAsync(text);
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

        UpdateChrome();
        _ = LoadHistoryAndSubscribeAsync();
    }

    private async Task HandleInputAsync(string text)
    {
        if (text.StartsWith('/'))
        {
            await HandleCommandAsync(text);
            return;
        }
        await SendMessageAsync(text);
    }

    private async Task HandleCommandAsync(string text)
    {
        var parts = text.Split(' ', 2, StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        var verb = parts[0].ToLowerInvariant();
        var arg = parts.Length > 1 ? parts[1] : null;

        switch (verb)
        {
            case "/help":
            case "/?":
                AppendOnUiThread(
                    "Commands:\n" +
                    "  /join #channel    switch to (or auto-create) a public channel\n" +
                    "  /dm <handle>      open a direct message with another user\n" +
                    "  /finger <handle>  print a user's profile inline\n" +
                    "  /quit             leave chat (back to lobby)\n" +
                    "  /help             show this help\n");
                return;

            case "/quit":
            case "/exit":
                _shutdown.Cancel();
                _app.RequestStop();
                return;

            case "/join":
                if (string.IsNullOrEmpty(arg))
                {
                    SetStatus("[!] /join requires a channel name (e.g. /join #random).");
                    return;
                }
                await SwitchToPublicAsync(arg);
                return;

            case "/dm":
                if (string.IsNullOrEmpty(arg))
                {
                    SetStatus("[!] /dm requires a handle (e.g. /dm alice).");
                    return;
                }
                await SwitchToDmAsync(arg);
                return;

            case "/finger":
                if (string.IsNullOrEmpty(arg))
                {
                    SetStatus("[!] /finger requires a handle (e.g. /finger alice).");
                    return;
                }
                await FingerAsync(arg);
                return;

            default:
                SetStatus($"[!] unknown command: {verb} — type /help for the list.");
                return;
        }
    }

    private async Task SwitchToPublicAsync(string channelName)
    {
        var chat = _services.GetRequiredService<ChatService>();
        var result = await chat.JoinPublicChannelAsync(channelName, _user.Id, _shutdown.Token);
        await ApplyJoinResultAsync(result);
    }

    private async Task SwitchToDmAsync(string handle)
    {
        var chat = _services.GetRequiredService<ChatService>();
        var result = await chat.JoinDmAsync(_user, handle, _shutdown.Token);
        await ApplyJoinResultAsync(result);
    }

    private async Task FingerAsync(string handle)
    {
        try
        {
            var profile = _services.GetRequiredService<ProfileService>();
            var snap = await profile.GetByHandleAsync(handle.Trim(), _shutdown.Token);
            if (snap is null)
            {
                AppendOnUiThread($"── finger {handle} ──\n   no such user.\n");
                return;
            }
            AppendOnUiThread(ProfileService.FormatFinger(snap));
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendOnUiThread($"[!] /finger failed: {ex.Message}\n");
        }
    }

    private async Task ApplyJoinResultAsync(ChatService.JoinResult result)
    {
        switch (result)
        {
            case ChatService.JoinResult.Joined j:
                await SwitchChannelAsync(j.Channel, "joined");
                return;
            case ChatService.JoinResult.Created c:
                await SwitchChannelAsync(c.Channel, "created");
                return;
            case ChatService.JoinResult.Denied d:
                SetStatus($"[!] {d.Reason}");
                return;
            case ChatService.JoinResult.InvalidName i:
                SetStatus($"[!] {i.Reason}");
                return;
            case ChatService.JoinResult.UserNotFound u:
                SetStatus($"[!] No user named '{u.Handle}'.");
                return;
        }
    }

    private async Task SwitchChannelAsync(Channel target, string verb)
    {
        if (target.Id == _currentChannel.Id)
        {
            SetStatus($"You're already in #{target.Name}.");
            return;
        }

        // Unhook the prior subscriber and wait for it to actually exit so the next pump doesn't
        // race against this one (two writers into _log at once would cause garbage).
        _channelCts.Cancel();
        if (_subscriber is not null)
        {
            try { await _subscriber.ConfigureAwait(false); }
            catch { /* expected on cancel */ }
        }
        _channelCts.Dispose();
        _channelCts = new CancellationTokenSource();

        _currentChannel = target;
        _app.Invoke(() =>
        {
            _log.Text = $"--- {verb} #{target.Name} ---\n";
            _log.MoveEnd();
            _log.SetNeedsDraw();
        });
        UpdateChrome();
        await LoadHistoryAndSubscribeAsync();
    }

    private async Task LoadHistoryAndSubscribeAsync()
    {
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var history = await db.ChatMessages
                .Where(m => m.ChannelId == _currentChannel.Id)
                .OrderByDescending(m => m.CreatedAt)
                .Take(20)
                .Include(m => m.User)
                .ToListAsync(_channelCts.Token);

            history.Reverse();
            foreach (var msg in history)
            {
                AppendOnUiThread(FormatMessage(msg.CreatedAt, msg.User?.Handle ?? "?", msg.Body));
            }

            var channelToken = CancellationTokenSource.CreateLinkedTokenSource(_shutdown.Token, _channelCts.Token).Token;
            _subscriber = Task.Run(() => RunSubscribeAsync(_currentChannel.Id, channelToken));
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
        catch (Exception ex)
        {
            AppendOnUiThread($"[!] history load failed: {ex.Message}\n");
        }
    }

    private async Task RunSubscribeAsync(long channelId, CancellationToken ct)
    {
        var bus = _services.GetRequiredService<IRealtimeBus>();
        try
        {
            await foreach (var payload in bus.SubscribeAsync(ChatTopics.Channel(channelId), ct))
            {
                ChatMessageDto? msg;
                try { msg = JsonSerializer.Deserialize<ChatMessageDto>(payload); } catch { continue; }
                if (msg is null) continue;
                AppendOnUiThread(FormatMessage(msg.CreatedAt, msg.Handle, msg.Body));
            }
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
    }

    private async Task SendMessageAsync(string body)
    {
        // Re-check access right before write so banning/private-channel revocation lands fast.
        var chat = _services.GetRequiredService<ChatService>();
        if (!await chat.CanAccessAsync(_currentChannel.Id, _user.Id, _shutdown.Token))
        {
            SetStatus("[!] You no longer have access to this channel.");
            return;
        }

        try
        {
            var now = DateTimeOffset.UtcNow;
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var msg = new ChatMessage
            {
                ChannelId = _currentChannel.Id,
                UserId = _user.Id,
                Body = body,
                CreatedAt = now,
            };
            db.ChatMessages.Add(msg);
            await db.SaveChangesAsync(_shutdown.Token);

            var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
            var dto = new ChatMessageDto(msg.Id, _currentChannel.Id, _user.Id, _user.Handle, body, now);
            await bus.PublishAsync(ChatTopics.Channel(_currentChannel.Id), JsonSerializer.SerializeToUtf8Bytes(dto), _shutdown.Token);
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendOnUiThread($"[!] send failed: {ex.Message}\n");
        }
    }

    private void UpdateChrome()
    {
        var label = _currentChannel.Name.StartsWith("dm-", StringComparison.Ordinal)
            ? "DM"
            : $"#{_currentChannel.Name}";
        Title = $"{label} — {_user.Handle} — /help — [Esc] back to lobby";
        SetStatus($"in {label}  topic: {_currentChannel.Topic ?? "(none)"}");
    }

    private void SetStatus(string text) => _app.Invoke(() => _status.Text = text);

    private void AppendOnUiThread(string text)
    {
        _app.Invoke(() =>
        {
            var current = _log.Text ?? string.Empty;
            _log.Text = current + text;
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
            try { _channelCts.Cancel(); } catch { /* ignore */ }
            _shutdown.Dispose();
            _channelCts.Dispose();
        }
        base.Dispose(disposing);
    }
}
