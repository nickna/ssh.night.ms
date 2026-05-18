using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Chat;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Slack-style "thread focus" view: one root message + its replies, with a composer that
// always posts as a reply to the root. Opens via `/thread <n>` from the parent ChatScreen
// and runs as a nested app.Run — when the user hits Esc, control returns to ChatScreen
// with its state intact (history, scroll, channel subscription).
//
// The subscriber here reads the *same* channel topic as the parent screen and filters
// envelope events to messages belonging to the focused thread (root id == _rootMessageId
// OR parent id == _rootMessageId). That keeps live replies/edits/deletes/reactions in
// sync without inventing a second topic.
public sealed class ChatThreadScreen : BbsWindow
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _user;
    private readonly long _channelId;
    private readonly long _rootMessageId;
    private readonly ChatLogView _log;
    private readonly TextField _input;
    private readonly BbsStatusLine _status;

    private readonly ChatMessageLog _msgLog = new();
    private readonly ChatEnvelopeDispatcher _dispatcher;
    private Task? _subscriber;

    public ChatThreadScreen(IServiceProvider services, IApplication app, User user, long channelId, long rootMessageId)
        : base(app, services, user)
    {
        _services = services;
        _app = app;
        _user = user;
        _channelId = channelId;
        _rootMessageId = rootMessageId;

        // Subscribe to the channel topic (same as ChatScreen) and filter events to those
        // belonging to this thread. The dispatcher owns deserialization + the type switch;
        // we hand it the per-event behaviour as delegates.
        _dispatcher = new ChatEnvelopeDispatcher
        {
            OnMessage = OnMessageEvent,
            OnEdit = OnEditEvent,
            OnDelete = OnDeleteEvent,
            OnPin = OnPinEvent,
            OnReaction = OnReactionEvent,
            // OnTopic intentionally omitted — topic events are channel-scoped chrome and
            // the thread view doesn't render them.
        };

        Title = $"thread — {_user.Handle} — [Esc] back to channel";

        _log = new ChatLogView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(3),
        };

        _status = new BbsStatusLine
        {
            X = 0,
            Y = Pos.Bottom(_log),
            Width = Dim.Fill(),
            Text = "loading thread…",
            DefaultKind = BbsStatusLine.StatusKind.Status,
        };

        _input = new TextField
        {
            X = 0,
            Y = Pos.Bottom(_status),
            Width = Dim.Fill(),
        };
        _input.SetScheme(BbsTheme.Input);

        _input.KeyDown += (_, key) =>
        {
            if (key == Key.Enter)
            {
                key.Handled = true;
                var text = (_input.Text ?? string.Empty).Trim();
                if (text.Length > 0)
                {
                    _input.Text = string.Empty;
                    HandleInputAsync(text).FireAndLog(_services, nameof(HandleInputAsync));
                }
                return;
            }
            if (key == Key.PageUp)        { key.Handled = true; _log.ScrollPage(-1); return; }
            if (key == Key.PageDown)      { key.Handled = true; _log.ScrollPage(+1); return; }
            if (key == Key.Home.WithCtrl) { key.Handled = true; _log.ScrollToTop(); return; }
            if (key == Key.End.WithCtrl)  { key.Handled = true; _log.ScrollToBottom(); return; }
        };

        Add(_log, _status, _input);
        _input.SetFocus();

        InstallEscapeHandler(() => ShutdownCts.Cancel());

        LoadAndSubscribeAsync().FireAndLog(_services, nameof(LoadAndSubscribeAsync));
    }

    private async Task LoadAndSubscribeAsync()
    {
        try
        {
            var muts = _services.GetRequiredService<ChatMutationService>();
            var thread = await muts.ListThreadAsync(_rootMessageId, Shutdown);
            if (thread.Root is null)
            {
                AppendSystem("[!] thread no longer exists.", isError: true);
                return;
            }

            var ids = new List<long> { thread.Root.Id };
            ids.AddRange(thread.Replies.Select(r => r.Id));
            var reactionMap = await muts.SnapshotReactionsAsync(ids, Shutdown);

            // Header: timeless separator + root message rendered as a normal message.
            var rootHandle = thread.Root.User?.Handle ?? "?";
            AppendSystem($"── thread by @{rootHandle} ── {thread.Replies.Count} repl{(thread.Replies.Count == 1 ? "y" : "ies")}");
            AddMessage(new MessageRef
            {
                MessageId = thread.Root.Id,
                Handle = rootHandle,
                At = thread.Root.CreatedAt,
                Body = thread.Root.Body,
                Edited = thread.Root.EditedAt is not null,
                Pinned = thread.Root.IsPinned,
                Deleted = thread.Root.DeletedAt is not null,
            });
            ApplyReactionSnapshot(thread.Root.Id, reactionMap);

            if (thread.Replies.Count == 0)
            {
                AppendSystem("─ no replies yet — be the first to reply below.");
            }
            else
            {
                AppendSystem($"── replies ({thread.Replies.Count}) ──");
            }

            foreach (var reply in thread.Replies)
            {
                AddMessage(new MessageRef
                {
                    MessageId = reply.Id,
                    Handle = reply.User?.Handle ?? "?",
                    At = reply.CreatedAt,
                    Body = reply.Body,
                    Edited = reply.EditedAt is not null,
                    Pinned = reply.IsPinned,
                    Deleted = reply.DeletedAt is not null,
                });
                ApplyReactionSnapshot(reply.Id, reactionMap);
            }

            SetStatus($"replying to @{rootHandle}  |  /help for commands  |  [Esc] back");
            _subscriber = Task.Run(() => RunSubscribeAsync(Shutdown));
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendSystem($"[!] load failed: {ex.Message}", isError: true);
        }
    }

    private async Task RunSubscribeAsync(CancellationToken ct)
    {
        var bus = _services.GetRequiredService<IRealtimeBus>();
        try
        {
            await foreach (var payload in bus.SubscribeAsync(ChatTopics.Channel(_channelId), ct))
            {
                DispatchEnvelope(payload);
            }
        }
        catch (OperationCanceledException) { /* expected on close */ }
    }

    private void DispatchEnvelope(byte[] payload) => _dispatcher.Dispatch(payload);

    // True for the thread root and any direct reply to it. Edit/delete/react/pin handlers
    // additionally gate on the message already being in _msgLog — events for other messages
    // in the same channel topic are simply ignored.
    private bool IsInThread(long messageId, long? parentMessageId) =>
        messageId == _rootMessageId || parentMessageId == _rootMessageId;

    private void OnMessageEvent(ChatMessageDto msg)
    {
        if (!IsInThread(msg.Id, msg.ParentMessageId)) return;
        AddMessage(new MessageRef
        {
            MessageId = msg.Id,
            Handle = msg.Handle,
            At = msg.CreatedAt,
            Body = msg.Body,
        });
    }

    private void OnEditEvent(ChatEditDto edit)
    {
        var msgRef = _msgLog.ApplyEdit(edit);
        if (msgRef is null) return;
        var newLine = RenderMessage(msgRef);
        _app.Invoke(() => _log.TryReplace(edit.MessageId, newLine));
    }

    private void OnDeleteEvent(ChatDeleteDto del)
    {
        var msgRef = _msgLog.ApplyDelete(del);
        if (msgRef is null) return;
        var line = MessageRenderer.RenderDeleted(_user.FormatClock(msgRef.At), msgRef.Handle);
        _app.Invoke(() => _log.TryReplace(del.MessageId, line));
    }

    private void OnPinEvent(ChatPinDto pin)
    {
        var msgRef = _msgLog.ApplyPin(pin);
        if (msgRef is null || msgRef.Deleted) return;
        var line = RenderMessage(msgRef);
        _app.Invoke(() => _log.TryReplace(pin.MessageId, line));
    }

    private void OnReactionEvent(ChatReactionDto react, bool add)
    {
        // Filter to messages on screen in this thread view so we don't accumulate state
        // for unrelated messages in the same channel.
        if (!_msgLog.Contains(react.MessageId)) return;
        _msgLog.ApplyReaction(react, add);
        PushReactionFooter(react.MessageId);
    }

    private void PushReactionFooter(long messageId)
    {
        var summaries = _msgLog.BuildSummaries(messageId, _user.Id);
        _app.Invoke(() => _log.TrySetReactions(messageId, summaries));
    }

    private void ApplyReactionSnapshot(long messageId, IReadOnlyDictionary<long, List<MessageReaction>> snap)
    {
        if (!snap.TryGetValue(messageId, out var rows)) return;
        _msgLog.SeedReactions(messageId, rows);
        PushReactionFooter(messageId);
    }

    private void AddMessage(MessageRef msgRef)
    {
        _msgLog.Add(msgRef);
        AppendOnUiThread(RenderMessage(msgRef), msgRef.MessageId);
    }

    private async Task HandleInputAsync(string text)
    {
        if (text.StartsWith('/'))
        {
            await HandleCommandAsync(text);
            return;
        }
        await SendReplyAsync(text);
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
                AppendSystem(
                    "Commands (thread):\n" +
                    "  Enter                post a reply to the root message\n" +
                    "  /me <action>         emote as a reply (italic)\n" +
                    "  /react <n> :emoji:   add a reaction to message n (1 = root, 2+ = replies)\n" +
                    "  /unreact <n> :emoji: remove your reaction from message n\n" +
                    "  /edit <n> <body>     edit your message at position n\n" +
                    "  /del <n>             delete your message at position n\n" +
                    "  /quit                back to channel\n" +
                    "Scrollback: PgUp / PgDn   |   jump: Ctrl+Home / Ctrl+End\n" +
                    "Esc returns to the channel view.");
                return;

            case "/quit":
            case "/exit":
                ShutdownCts.Cancel();
                _app.RequestStop();
                return;

            case "/me":
                if (string.IsNullOrEmpty(arg)) { SetStatus("[!] /me requires an action."); return; }
                await SendReplyAsync("/me " + arg);
                return;

            case "/react":
                await ReactAsync(arg, add: true);
                return;

            case "/unreact":
                await ReactAsync(arg, add: false);
                return;

            case "/edit":
                await EditAsync(arg);
                return;

            case "/del":
            case "/delete":
                await DeleteAsync(arg);
                return;

            default:
                SetStatus($"[!] unknown command in thread: {verb} — type /help.");
                return;
        }
    }

    private async Task ReactAsync(string? arg, bool add)
    {
        if (string.IsNullOrEmpty(arg) || !TryParseReactArg(arg, out var pos, out var emojiText))
        {
            SetStatus("[!] usage: /react <n> :emoji:");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        var emoji = EmojiTable.Substitute(emojiText);
        if (emoji.Contains(':'))
        {
            SetStatus($"[!] unknown emoji shortcode: {emojiText}");
            return;
        }
        var muts = _services.GetRequiredService<ChatMutationService>();
        var result = add
            ? await muts.ReactAsync(msgRef.MessageId, _user.Id, _user.Handle, emoji, Shutdown)
            : await muts.UnreactAsync(msgRef.MessageId, _user.Id, _user.Handle, emoji, Shutdown);
        ReportMutation(result);
    }

    private async Task EditAsync(string? arg)
    {
        if (string.IsNullOrEmpty(arg) || !TryParsePositionArg(arg, out var pos, out var newBody) || string.IsNullOrWhiteSpace(newBody))
        {
            SetStatus("[!] usage: /edit <n> <new body>");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        var muts = _services.GetRequiredService<ChatMutationService>();
        var result = await muts.EditAsync(msgRef.MessageId, _user.Id, newBody, Shutdown);
        ReportMutation(result);
    }

    private async Task DeleteAsync(string? arg)
    {
        if (string.IsNullOrEmpty(arg) || !int.TryParse(arg, out var pos))
        {
            SetStatus("[!] usage: /del <n>");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        var muts = _services.GetRequiredService<ChatMutationService>();
        var result = await muts.DeleteAsync(msgRef.MessageId, _user.Id, Shutdown);
        ReportMutation(result);
    }

    private void ReportMutation(ChatOpResult result)
    {
        switch (result)
        {
            case ChatOpResult.Ok: SetStatus("ok"); return;
            case ChatOpResult.NotFound: SetStatus("[!] message not found."); return;
            case ChatOpResult.Forbidden f: SetStatus($"[!] {f.Reason}"); return;
            case ChatOpResult.Invalid i: SetStatus($"[!] {i.Reason}"); return;
        }
    }

    // Position-based — the root is index 1, replies follow in chronological order. Mirrors
    // ChatScreen's command UX so muscle memory carries over.
    private bool TryResolveMessage(int positionOneBased, out MessageRef msgRef)
    {
        msgRef = default!;
        var idx = positionOneBased - 1;
        if (idx < 0 || idx >= _msgLog.Count) return false;
        msgRef = _msgLog.Messages[idx];
        return true;
    }

    private static bool TryParseReactArg(string arg, out int position, out string emoji)
    {
        position = 0;
        emoji = string.Empty;
        var parts = arg.Split(' ', 2, StringSplitOptions.TrimEntries | StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length != 2) return false;
        if (!int.TryParse(parts[0], out position)) return false;
        emoji = parts[1];
        return position > 0 && emoji.Length > 0;
    }

    private static bool TryParsePositionArg(string arg, out int position, out string rest)
    {
        position = 0;
        rest = string.Empty;
        var parts = arg.Split(' ', 2, StringSplitOptions.TrimEntries | StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length < 1) return false;
        if (!int.TryParse(parts[0], out position)) return false;
        rest = parts.Length > 1 ? parts[1] : string.Empty;
        return position > 0;
    }

    private async Task SendReplyAsync(string body)
    {
        try
        {
            var now = DateTimeOffset.UtcNow;
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var msg = new ChatMessage
            {
                ChannelId = _channelId,
                UserId = _user.Id,
                Body = body,
                CreatedAt = now,
                ParentMessageId = _rootMessageId,
            };
            db.ChatMessages.Add(msg);
            await db.SaveChangesAsync(Shutdown);

            var bus = scope.ServiceProvider.GetRequiredService<IRealtimeBus>();
            var dto = new ChatMessageDto(msg.Id, _channelId, _user.Id, _user.Handle, body, now, _rootMessageId);
            var envelope = new ChatEnvelope(ChatEventKind.Message, JsonSerializer.SerializeToElement(dto));
            await bus.PublishAsync(ChatTopics.Channel(_channelId), JsonSerializer.SerializeToUtf8Bytes(envelope), Shutdown);
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendSystem($"[!] reply failed: {ex.Message}", isError: true);
        }
    }

    private ChatLine RenderMessage(MessageRef msgRef)
    {
        var clock = _user.FormatClock(msgRef.At);
        if (msgRef.Deleted)
        {
            return MessageRenderer.RenderDeleted(clock, msgRef.Handle);
        }
        if (msgRef.Body is not null && msgRef.Body.StartsWith("/me ", StringComparison.Ordinal))
        {
            return MessageRenderer.RenderEmote(clock, msgRef.Handle, msgRef.Body[4..], _user.Handle);
        }
        // In the thread view, replies are visually obvious from the layout (root header +
        // ── replies ── separator), so we deliberately suppress the inline "↳ @handle" prefix
        // that the channel view uses. Root keeps the pinned glyph if set; replies don't show
        // a reply-count badge (they're already inside their parent's count).
        return MessageRenderer.RenderMessage(clock, msgRef.Handle, msgRef.Body ?? string.Empty, _user.Handle,
            edited: msgRef.Edited, pinned: msgRef.Pinned,
            replyToHandle: null, replyCount: 0);
    }

    private void AppendSystem(string text, bool isError = false) =>
        AppendOnUiThread(MessageRenderer.RenderSystem(text, isError));

    private void AppendOnUiThread(ChatLine line, long? messageId = null) =>
        _app.Invoke(() => _log.Append(line, messageId));

    private void SetStatus(string text) => _app.Invoke(() => _status.Set(text));


}
