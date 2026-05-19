using System.Text;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshServer.Tui.Chat;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Chat;

// Minimal table-chat pane for the Hold'em screen. Reuses ChatMutationService for sending
// and ChatEnvelopeDispatcher for receiving — same wire format as the full ChatScreen,
// just rendered into a tiny scrollable log instead of the full chat surface.
//
// System messages (game actions echoed by HoldemScreen) are local-only: they go into the
// log via AppendSystem and never hit the bus. So spectators at the same table see the
// same player chat, but each session draws its own action narration from the game events
// it already subscribes to.
internal sealed class TableChatPane : View
{
    private const int MaxLogLines = 80;   // keeps the in-memory ring bounded; the visible
                                          // window scrolls inside the TextView.

    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly long _channelId;
    private readonly IRealtimeBus _bus;
    private readonly ChatMutationService _mutator;
    private readonly ILogger<TableChatPane> _log;

    private readonly TextView _logView;
    private readonly TextField _input;
    private readonly List<string> _lines = new();

    private CancellationTokenSource? _cts;
    private Task? _subTask;
    private readonly object _linesLock = new();

    public TableChatPane(
        IApplication app,
        IServiceProvider services,
        User user,
        long channelId)
    {
        _app = app;
        _services = services;
        _user = user;
        _channelId = channelId;
        _bus = services.GetRequiredService<IRealtimeBus>();
        _mutator = services.GetRequiredService<ChatMutationService>();
        _log = services.GetRequiredService<ILoggerFactory>().CreateLogger<TableChatPane>();

        BorderStyle = LineStyle.Single;
        Title = "table chat";
        CanFocus = false;

        _logView = new TextView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(1),
            ReadOnly = true,
            CanFocus = false,
            WordWrap = false,
        };
        _input = new TextField
        {
            X = 0,
            Y = Pos.AnchorEnd(1),
            Width = Dim.Fill(),
            Height = 1,
        };
        _input.KeyDown += OnInputKey;

        Add(_logView, _input);

        StartSubscription();
    }

    // -- Public surface ----------------------------------------------------------------

    public void FocusInput() => SafeInvoke(() => _input.SetFocus());

    public bool IsInputFocused => _input.HasFocus;

    public void AppendSystem(string text) => AppendLine($"[~] {text}", system: true);

    // -- Subscription ------------------------------------------------------------------

    private void StartSubscription()
    {
        _cts = new CancellationTokenSource();
        var dispatcher = new ChatEnvelopeDispatcher
        {
            OnMessage = m =>
            {
                if (m.ChannelId != _channelId) return;
                AppendLine($"<{m.Handle}> {m.Body}", system: false);
            },
        };
        _subTask = Task.Run(() => RunSubscribeAsync(dispatcher, _cts.Token));
    }

    private async Task RunSubscribeAsync(ChatEnvelopeDispatcher dispatcher, CancellationToken ct)
    {
        // Reconnect with backoff. Chat is best-effort by design — we don't try to backfill
        // missed messages on resubscribe (the durable history is in chat_messages; users
        // can scroll up later) but we do want the live feed to come back up.
        var backoffMs = 500;
        while (!ct.IsCancellationRequested)
        {
            var disconnected = false;
            try
            {
                await foreach (var payload in _bus.SubscribeAsync(ChatTopics.Channel(_channelId), ct))
                {
                    backoffMs = 500;
                    try { dispatcher.Dispatch(payload); }
                    catch (Exception ex) { _log.LogError(ex, "chat dispatch failed"); }
                }
                disconnected = true;
            }
            catch (OperationCanceledException) { return; }
            catch (Exception ex)
            {
                _log.LogWarning(ex, "chat subscribe dropped; reconnecting");
                disconnected = true;
            }
            if (!disconnected || ct.IsCancellationRequested) return;
            AppendLine($"[~] chat reconnecting…", system: true);
            try { await Task.Delay(backoffMs, ct); } catch (OperationCanceledException) { return; }
            backoffMs = Math.Min(backoffMs * 2, 5000);
        }
    }

    // -- Rendering ---------------------------------------------------------------------

    private void AppendLine(string line, bool system)
    {
        lock (_linesLock)
        {
            _lines.Add(line);
            if (_lines.Count > MaxLogLines)
                _lines.RemoveRange(0, _lines.Count - MaxLogLines);
        }
        SafeInvoke(() =>
        {
            string text;
            lock (_linesLock)
            {
                var sb = new StringBuilder();
                foreach (var l in _lines) sb.AppendLine(l);
                text = sb.ToString();
            }
            _logView.Text = text;
            // Scroll to bottom — TextView exposes MoveEnd for this.
            _logView.MoveEnd();
        });
    }

    // Marshals an action onto the UI thread without throwing if the IApplication has
    // already been disposed. The subscriber tasks (bus, dispatcher) keep running for a
    // short window after the screen exits — until the cancellation token propagates —
    // and any message that lands in that window would otherwise raise
    // NotInitializedException out of Application.Invoke. Dropping the UI update during
    // teardown is correct: there is no view left to draw onto.
    private void SafeInvoke(Action a)
    {
        try { _app.Invoke(a); }
        catch (NotInitializedException) { }
        catch (InvalidOperationException) { }
    }

    // -- Input handling ----------------------------------------------------------------

    private void OnInputKey(object? sender, Key key)
    {
        if (key == Key.Enter)
        {
            key.Handled = true;
            var text = _input.Text?.ToString() ?? string.Empty;
            if (string.IsNullOrWhiteSpace(text))
            {
                _input.Text = string.Empty;
                return;
            }
            _input.Text = string.Empty;
            // Fire-and-forget post; failures show in the log line. We don't await on the
            // UI thread because that would block the input field redraw.
            _ = PostAsync(text);
        }
        else if (key == Key.Tab || key == Key.Esc)
        {
            // Bounce focus back to the surrounding screen. The parent decides where to
            // redirect — we just give up the focus here.
            key.Handled = true;
            SuperView?.SetFocus();
        }
    }

    private async Task PostAsync(string body)
    {
        try
        {
            var result = await _mutator.PostAsync(_channelId, _user.Id, _user.Handle, body, parentMessageId: null, CancellationToken.None);
            if (result is ChatOpResult.Invalid inv)
                AppendLine($"[!] {inv.Reason}", system: true);
            else if (result is ChatOpResult.Forbidden f)
                AppendLine($"[!] {f.Reason}", system: true);
        }
        catch (Exception ex)
        {
            _log.LogError(ex, "chat post failed");
            AppendLine($"[!] send failed: {ex.Message}", system: true);
        }
    }

    // -- Cleanup -----------------------------------------------------------------------

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            try { _cts?.Cancel(); } catch { }
            _cts?.Dispose();
        }
        base.Dispose(disposing);
    }
}
