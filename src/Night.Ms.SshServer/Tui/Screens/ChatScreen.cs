using System.Collections.Concurrent;
using System.Collections.ObjectModel;
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

public sealed class ChatScreen : BbsWindow
{
    // History loaded on join. 100 covers a typical channel re-entry comfortably without
    // pulling enough rows to noticeably delay the channel switch.
    private const int HistoryRowCount = 100;
    // Right (members) sidebar; left (channels) sidebar uses ChannelPaneWidth.
    private const int SidebarWidth = 16;
    private const int ChannelPaneWidth = 20;
    private static readonly TimeSpan ChannelRefreshPeriod = TimeSpan.FromSeconds(5);
    private static readonly TimeSpan HeartbeatPeriod = TimeSpan.FromSeconds(10);
    private static readonly TimeSpan PresencePollPeriod = TimeSpan.FromSeconds(15);
    // Typing publish rate-limit. Two seconds means a 50wpm typer fans out ~3-4 events per
    // minute per channel instead of 50+ per second.
    private static readonly TimeSpan TypingDebounce = TimeSpan.FromSeconds(2);
    // How long a typing hint stays visible after the last event from that user.
    private static readonly TimeSpan TypingFade = TimeSpan.FromSeconds(4);

    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _user;
    private readonly ChatLogView _log;
    private readonly FrameView _sidebar;
    private readonly ListView _sidebarList;
    private readonly FrameView _channelsPane;
    private readonly ListView _channelsList;
    private readonly TextField _input;
    private readonly Label _status;
    private readonly CancellationTokenSource _shutdown = new();
    private readonly ConcurrentDictionary<long, string> _drafts = new();

    // Tracks the messages we've displayed, newest-first, so /react /edit /del <n> can map a
    // position to a real ChatMessage.Id. Also seeds the reactions map for snapshot rendering.
    private readonly List<MessageRef> _recent = new();
    private readonly Dictionary<long, Dictionary<string, HashSet<long>>> _reactions = new();

    // Active "typing…" hints — handle → last-seen-typing-at. Pruned each tick of _typingTimer.
    private readonly Dictionary<string, DateTimeOffset> _typers = new();
    private DateTimeOffset _lastTypingPublishedAt = DateTimeOffset.MinValue;
    private string _typingHint = string.Empty;

    // Current sidebar contents. _channelEntries lives at Screen scope (not per-channel) so
    // Alt+digit can switch without re-querying. Rebuilt by RefreshChannelsAsync.
    private IReadOnlyList<ReadStateService.ChannelEntry> _channelEntries = Array.Empty<ReadStateService.ChannelEntry>();
    // Highest message id seen in the current channel, used to bump the read pointer.
    private long _lastReadMessageId;

    private Channel _currentChannel;
    private CancellationTokenSource _channelCts = new();
    private Task? _subscriber;
    private Task? _presenceSubscriber;
    private Task? _heartbeat;
    private Task? _typingPrune;
    private Task? _channelsRefresh;

    public ChatScreen(IServiceProvider services, IApplication app, User user, Channel initialChannel)
        : base(app, services, user)
    {
        _services = services;
        _app = app;
        _user = user;
        _currentChannel = initialChannel;

        _channelsPane = new FrameView
        {
            X = 0,
            Y = 0,
            Width = ChannelPaneWidth,
            Height = Dim.Fill(3),
            Title = "channels",
        };
        _channelsPane.SetScheme(BbsTheme.Hint);

        _channelsList = new ListView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(),
        };
        _channelsPane.Add(_channelsList);

        _log = new ChatLogView
        {
            X = Pos.Right(_channelsPane) + 1,
            Y = 0,
            // Leave room for the right sidebar (members) + 1-col gap on each side.
            Width = Dim.Fill(SidebarWidth + 1),
            Height = Dim.Fill(3),
        };

        _sidebar = new FrameView
        {
            X = Pos.Right(_log) + 1,
            Y = 0,
            Width = SidebarWidth,
            Height = Dim.Fill(3),
            Title = "online",
        };
        _sidebar.SetScheme(BbsTheme.Hint);

        _sidebarList = new ListView
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Height = Dim.Fill(),
        };
        _sidebar.Add(_sidebarList);

        _status = new Label
        {
            X = 0,
            Y = Pos.Bottom(_log),
            Width = Dim.Fill(),
        };
        _status.SetScheme(BbsTheme.Status);

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
                    _drafts.TryRemove(_currentChannel.Id, out string? _);
                    _ = HandleInputAsync(text);
                }
                return;
            }
            if (key == Key.PageUp)        { key.Handled = true; _log.ScrollPage(-1); return; }
            if (key == Key.PageDown)      { key.Handled = true; _log.ScrollPage(+1); return; }
            if (key == Key.Home.WithCtrl) { key.Handled = true; _log.ScrollToTop(); return; }
            if (key == Key.End.WithCtrl)  { key.Handled = true; _log.ScrollToBottom(); return; }

            // Every non-scroll, non-Enter keystroke is a typing signal. Debounced inside
            // MaybePublishTypingAsync so we don't fan out 1 event per character.
            _ = MaybePublishTypingAsync();
        };

        Add(_channelsPane, _log, _sidebar, _status, _input);
        _input.SetFocus();

        KeyDown += (_, key) =>
        {
            if (key == Key.Esc)
            {
                key.Handled = true;
                _shutdown.Cancel();
                _app.RequestStop();
                return;
            }
            // Alt+1..Alt+9 jumps to the Nth channel in the sidebar; Alt+0 is the 10th slot.
            // Alt+digit is universal across PuTTY/WT/iTerm; plain digits would conflict with
            // input typing.
            var slot = ChannelSlotForAltKey(key);
            if (slot is not null)
            {
                key.Handled = true;
                _ = SwitchByIndexAsync(slot.Value);
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
                AppendSystem(
                    "Commands:\n" +
                    "  /join #channel       switch to (or auto-create) a public channel\n" +
                    "  /dm <handle>         open a direct message with another user\n" +
                    "  /me <action>         emote in third-person (italic)\n" +
                    "  /react <n> :emoji:   add a reaction to message n (1 = most recent)\n" +
                    "  /unreact <n> :emoji: remove your reaction from message n\n" +
                    "  /edit <n> <body>     edit your message at position n\n" +
                    "  /del <n>             delete your message at position n\n" +
                    "  /pin <n>             pin message n (★ marker, listed by /pins)\n" +
                    "  /unpin <n>           remove the pin marker\n" +
                    "  /pins                list all pinned messages in this channel\n" +
                    "  /topic <text>        set the channel topic (channel creator only)\n" +
                    "  /search <term>       search recent messages in this channel\n" +
                    "  /who                 show who's in this channel\n" +
                    "  /finger <handle>     print a user's profile inline\n" +
                    "  /quit                leave chat (back to lobby)\n" +
                    "  /help                show this help\n" +
                    "Formatting: *bold*  _italic_  `code`  @mention  :emoji:\n" +
                    "Scrollback: PgUp / PgDn   |   jump to ends: Ctrl+Home / Ctrl+End\n" +
                    "Switch channel: Alt+1..Alt+9 (slot number in left sidebar; Alt+0 = 10)");
                return;

            case "/quit":
            case "/exit":
                _shutdown.Cancel();
                _app.RequestStop();
                return;

            case "/join":
                if (string.IsNullOrEmpty(arg)) { SetStatus("[!] /join requires a channel name (e.g. /join #random)."); return; }
                await SwitchToPublicAsync(arg);
                return;

            case "/dm":
                if (string.IsNullOrEmpty(arg)) { SetStatus("[!] /dm requires a handle (e.g. /dm alice)."); return; }
                await SwitchToDmAsync(arg);
                return;

            case "/me":
                if (string.IsNullOrEmpty(arg)) { SetStatus("[!] /me requires an action (e.g. /me waves)."); return; }
                await SendMessageAsync("/me " + arg);
                return;

            case "/finger":
                if (string.IsNullOrEmpty(arg)) { SetStatus("[!] /finger requires a handle (e.g. /finger alice)."); return; }
                await FingerAsync(arg);
                return;

            case "/who":
                await WhoAsync();
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

            case "/pin":
                await PinAsync(arg, pin: true);
                return;

            case "/unpin":
                await PinAsync(arg, pin: false);
                return;

            case "/pins":
                await ListPinsAsync();
                return;

            case "/topic":
                await SetTopicAsync(arg);
                return;

            case "/search":
                await SearchAsync(arg);
                return;

            default:
                SetStatus($"[!] unknown command: {verb} — type /help for the list.");
                return;
        }
    }

    // Index into the rendered _channelEntries list. Triggered by Alt+digit and by clicking
    // a channel in the sidebar.
    private async Task SwitchByIndexAsync(int slot)
    {
        if (slot < 0 || slot >= _channelEntries.Count) return;
        var entry = _channelEntries[slot];
        if (entry.ChannelId == _currentChannel.Id) return;

        // Re-fetch the Channel row so we get the live Topic + CreatedById (we only kept
        // metadata in the sidebar entry, not the full entity).
        try
        {
            await using var scope = _services.CreateAsyncScope();
            var db = scope.ServiceProvider.GetRequiredService<AppDbContext>();
            var target = await db.Channels.AsNoTracking().FirstOrDefaultAsync(c => c.Id == entry.ChannelId, _shutdown.Token);
            if (target is null) { SetStatus("[!] channel no longer exists."); return; }
            await SwitchChannelAsync(target, "joined");
        }
        catch (Exception ex)
        {
            SetStatus($"[!] switch failed: {ex.Message}");
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
                AppendSystem($"── finger {handle} ──\n   no such user.");
                return;
            }
            var lines = ProfileService.FormatFinger(snap, _user).TrimEnd('\n').Split('\n');
            foreach (var line in lines)
            {
                AppendOnUiThread(MessageRenderer.RenderRaw(line));
            }
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendSystem($"[!] /finger failed: {ex.Message}", isError: true);
        }
    }

    private async Task WhoAsync()
    {
        try
        {
            var presence = _services.GetRequiredService<PresenceService>();
            var members = await presence.ListAsync(_currentChannel.Id, _shutdown.Token);
            if (members.Count == 0)
            {
                AppendSystem("─ online: (nobody else)");
                return;
            }
            AppendSystem("─ online: " + string.Join(", ", members));
        }
        catch (Exception ex)
        {
            AppendSystem($"[!] /who failed: {ex.Message}", isError: true);
        }
    }

    // /react <n> :emoji:  — adds an emoji reaction to the n-th most recent message.
    private async Task ReactAsync(string? arg, bool add)
    {
        if (string.IsNullOrEmpty(arg) || !TryParseReactArg(arg, out var pos, out var emojiText))
        {
            SetStatus("[!] usage: /react <n> :emoji: (e.g. /react 1 :fire:)");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        var emoji = EmojiTable.Substitute(emojiText);
        // Reject unsubstituted shortcodes — we don't want raw ":foo:" text on the wire.
        if (emoji.Contains(':'))
        {
            SetStatus($"[!] unknown emoji shortcode: {emojiText}");
            return;
        }

        var muts = _services.GetRequiredService<ChatMutationService>();
        var result = add
            ? await muts.ReactAsync(msgRef.MessageId, _user.Id, _user.Handle, emoji, _shutdown.Token)
            : await muts.UnreactAsync(msgRef.MessageId, _user.Id, _user.Handle, emoji, _shutdown.Token);
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
        var result = await muts.EditAsync(msgRef.MessageId, _user.Id, newBody, _shutdown.Token);
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
        var result = await muts.DeleteAsync(msgRef.MessageId, _user.Id, _shutdown.Token);
        ReportMutation(result);
    }

    private async Task PinAsync(string? arg, bool pin)
    {
        if (string.IsNullOrEmpty(arg) || !int.TryParse(arg, out var pos))
        {
            SetStatus($"[!] usage: /{(pin ? "pin" : "unpin")} <n>");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        var muts = _services.GetRequiredService<ChatMutationService>();
        var result = pin
            ? await muts.PinAsync(msgRef.MessageId, _user.Id, _shutdown.Token)
            : await muts.UnpinAsync(msgRef.MessageId, _user.Id, _shutdown.Token);
        ReportMutation(result);
    }

    private async Task ListPinsAsync()
    {
        try
        {
            var muts = _services.GetRequiredService<ChatMutationService>();
            var pins = await muts.ListPinsAsync(_currentChannel.Id, _shutdown.Token);
            if (pins.Count == 0)
            {
                AppendSystem("─ no pinned messages in this channel.");
                return;
            }
            AppendSystem($"─ pinned ({pins.Count}):");
            foreach (var p in pins)
            {
                var preview = p.Body.Length > 80 ? p.Body[..80] + "…" : p.Body;
                AppendOnUiThread(MessageRenderer.RenderMessage(
                    _user.FormatClock(p.CreatedAt),
                    p.User?.Handle ?? "?",
                    preview,
                    _user.Handle,
                    edited: p.EditedAt is not null,
                    pinned: true));
            }
        }
        catch (Exception ex)
        {
            AppendSystem($"[!] /pins failed: {ex.Message}", isError: true);
        }
    }

    private async Task SetTopicAsync(string? arg)
    {
        var muts = _services.GetRequiredService<ChatMutationService>();
        var result = await muts.SetTopicAsync(_currentChannel.Id, _user.Id, _user.Handle, arg, _shutdown.Token);
        ReportMutation(result);
    }

    // /search <term>. Last 50 matches in the current channel, newest first. The renderer
    // reuses RenderMessage so colors/formatting/pin markers stay consistent.
    private async Task SearchAsync(string? arg)
    {
        if (string.IsNullOrWhiteSpace(arg))
        {
            SetStatus("[!] usage: /search <term>");
            return;
        }
        try
        {
            var muts = _services.GetRequiredService<ChatMutationService>();
            var hits = await muts.SearchAsync(_currentChannel.Id, arg, limit: 50, _shutdown.Token);
            if (hits.Count == 0)
            {
                AppendSystem($"─ no matches for \"{arg}\".");
                return;
            }
            AppendSystem($"─ search \"{arg}\" — {hits.Count} match{(hits.Count == 1 ? "" : "es")}:");
            // Show oldest match first so the visual reads top-to-bottom-old-to-new like
            // a normal log scroll.
            foreach (var m in hits.Reverse())
            {
                AppendOnUiThread(MessageRenderer.RenderMessage(
                    _user.FormatClock(m.CreatedAt),
                    m.User?.Handle ?? "?",
                    m.Body,
                    _user.Handle,
                    edited: m.EditedAt is not null,
                    pinned: m.IsPinned));
            }
        }
        catch (Exception ex)
        {
            AppendSystem($"[!] /search failed: {ex.Message}", isError: true);
        }
    }

    private void ReportMutation(ChatMutationService.Result result)
    {
        switch (result)
        {
            case ChatMutationService.Result.Ok: SetStatus("ok"); return;
            case ChatMutationService.Result.NotFound: SetStatus("[!] message not found."); return;
            case ChatMutationService.Result.Forbidden f: SetStatus($"[!] {f.Reason}"); return;
            case ChatMutationService.Result.Invalid i: SetStatus($"[!] {i.Reason}"); return;
        }
    }

    // Parses "<n> :emoji:" / "<n> some-emoji-glyph"; returns (position, emoji-as-typed).
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

    private bool TryResolveMessage(int positionOneBased, out MessageRef msgRef)
    {
        msgRef = default!;
        var idx = positionOneBased - 1;
        if (idx < 0 || idx >= _recent.Count) return false;
        msgRef = _recent[idx];
        return true;
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

        var stashed = (_input.Text ?? string.Empty).ToString();
        if (!string.IsNullOrEmpty(stashed)) _drafts[_currentChannel.Id] = stashed;
        _app.Invoke(() => _input.Text = string.Empty);

        // Let the prior channel's presence/message/heartbeat tasks know we're moving.
        await TeardownChannelTasksAsync();

        // Inform the prior channel we left, then move state.
        await LeaveChannelPresenceAsync(_currentChannel.Id);
        _channelCts.Dispose();
        _channelCts = new CancellationTokenSource();

        _currentChannel = target;
        _recent.Clear();
        _reactions.Clear();
        var label = LabelFor(target);
        _app.Invoke(() =>
        {
            _log.Clear();
            _log.Append(MessageRenderer.RenderSystem($"--- {verb} {label} ---"));
            if (_drafts.TryGetValue(target.Id, out var draft)) _input.Text = draft;
        });
        UpdateChrome();
        await LoadHistoryAndSubscribeAsync();
    }

    private async Task TeardownChannelTasksAsync()
    {
        _channelCts.Cancel();
        var tasks = new List<Task>();
        if (_subscriber is not null)         tasks.Add(_subscriber);
        if (_presenceSubscriber is not null) tasks.Add(_presenceSubscriber);
        if (_heartbeat is not null)          tasks.Add(_heartbeat);
        if (_typingPrune is not null)        tasks.Add(_typingPrune);
        if (_channelsRefresh is not null)    tasks.Add(_channelsRefresh);
        try { await Task.WhenAll(tasks).ConfigureAwait(false); }
        catch { /* expected on cancel */ }
        _subscriber = null;
        _presenceSubscriber = null;
        _heartbeat = null;
        _typingPrune = null;
        _channelsRefresh = null;
        lock (_typers) _typers.Clear();
        _typingHint = string.Empty;
    }

    private async Task LeaveChannelPresenceAsync(long channelId)
    {
        try
        {
            var presence = _services.GetRequiredService<PresenceService>();
            await presence.LeaveAsync(channelId, _user.Id, _user.Handle, _shutdown.Token);
        }
        catch { /* presence is best-effort */ }
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
                .Take(HistoryRowCount)
                .Include(m => m.User)
                .ToListAsync(_channelCts.Token);

            history.Reverse();

            // Pull reactions snapshot for the visible history so the initial render shows
            // current totals without waiting for live events to redraw.
            var muts = scope.ServiceProvider.GetRequiredService<ChatMutationService>();
            var reactionMap = await muts.SnapshotReactionsAsync(
                history.Select(m => m.Id).ToArray(), _channelCts.Token);

            long highestSeenId = 0;
            foreach (var msg in history)
            {
                var handle = msg.User?.Handle ?? "?";
                var msgRef = new MessageRef
                {
                    MessageId = msg.Id,
                    Handle = handle,
                    At = msg.CreatedAt,
                    Body = msg.Body,
                    Edited = msg.EditedAt is not null,
                    Pinned = msg.IsPinned,
                    Deleted = msg.DeletedAt is not null,
                };
                _recent.Insert(0, msgRef);
                AppendOnUiThread(RenderMessage(msgRef), msg.Id);
                if (reactionMap.TryGetValue(msg.Id, out var rows))
                {
                    SeedReactions(msg.Id, rows);
                    PushReactionFooter(msg.Id);
                }
                if (msg.Id > highestSeenId) highestSeenId = msg.Id;
            }
            _lastReadMessageId = highestSeenId;
            // Mark the channel read at the highest id we just displayed. Done in the
            // background so a slow DB doesn't delay the first render of the chat.
            if (highestSeenId > 0)
            {
                _ = MarkReadSafelyAsync(_currentChannel.Id, highestSeenId);
            }

            var channelToken = CancellationTokenSource.CreateLinkedTokenSource(_shutdown.Token, _channelCts.Token).Token;
            _subscriber = Task.Run(() => RunSubscribeAsync(_currentChannel.Id, channelToken));
            _presenceSubscriber = Task.Run(() => RunPresenceSubscribeAsync(_currentChannel.Id, channelToken));
            _heartbeat = Task.Run(() => RunHeartbeatAsync(_currentChannel.Id, channelToken));
            _typingPrune = Task.Run(() => RunTypingPruneAsync(channelToken));
            _channelsRefresh = Task.Run(() => RunChannelsRefreshAsync(channelToken));

            // Announce ourselves into the channel's presence set, then refresh both
            // sidebars from authoritative state.
            var presence = _services.GetRequiredService<PresenceService>();
            await presence.JoinAsync(_currentChannel.Id, _user.Id, _user.Handle, _shutdown.Token);
            await RefreshSidebarAsync(channelToken);
            await RefreshChannelsAsync(channelToken);
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
        catch (Exception ex)
        {
            AppendSystem($"[!] history load failed: {ex.Message}", isError: true);
        }
    }

    private async Task RunSubscribeAsync(long channelId, CancellationToken ct)
    {
        var bus = _services.GetRequiredService<IRealtimeBus>();
        try
        {
            await foreach (var payload in bus.SubscribeAsync(ChatTopics.Channel(channelId), ct))
            {
                DispatchChatEnvelope(payload);
            }
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
    }

    private void DispatchChatEnvelope(byte[] payload)
    {
        ChatEnvelope? envelope;
        try { envelope = JsonSerializer.Deserialize<ChatEnvelope>(payload); }
        catch { return; }
        if (envelope is null) return;

        switch (envelope.Kind)
        {
            case ChatEventKind.Message:
                if (TryDeserialize<ChatMessageDto>(envelope.Payload, out var msg))
                {
                    var newRef = new MessageRef
                    {
                        MessageId = msg.Id,
                        Handle = msg.Handle,
                        At = msg.CreatedAt,
                        Body = msg.Body,
                    };
                    _recent.Insert(0, newRef);
                    AppendOnUiThread(RenderMessage(newRef), msg.Id);
                    // The sender is implicitly no longer typing — clear their hint so the
                    // status bar doesn't say "alice is typing…" right after alice posts.
                    ClearTyperOnUiThread(msg.Handle);
                    // We're looking at the channel — bump the read pointer so the unread
                    // badge stays at zero. Fire-and-forget to keep the envelope dispatcher
                    // synchronous; failures are surfaced by the next refresh.
                    if (msg.Id > _lastReadMessageId)
                    {
                        _lastReadMessageId = msg.Id;
                        _ = MarkReadSafelyAsync(_currentChannel.Id, msg.Id);
                    }
                }
                return;
            case ChatEventKind.Edit:
                if (TryDeserialize<ChatEditDto>(envelope.Payload, out var edit))
                {
                    ApplyEdit(edit);
                }
                return;
            case ChatEventKind.Delete:
                if (TryDeserialize<ChatDeleteDto>(envelope.Payload, out var del))
                {
                    ApplyDelete(del);
                }
                return;
            case ChatEventKind.React:
                if (TryDeserialize<ChatReactionDto>(envelope.Payload, out var react))
                {
                    ApplyReaction(react, add: true);
                }
                return;
            case ChatEventKind.Unreact:
                if (TryDeserialize<ChatReactionDto>(envelope.Payload, out var unreact))
                {
                    ApplyReaction(unreact, add: false);
                }
                return;
            case ChatEventKind.Pin:
            case ChatEventKind.Unpin:
                if (TryDeserialize<ChatPinDto>(envelope.Payload, out var pin))
                {
                    ApplyPin(pin);
                }
                return;
            case ChatEventKind.Topic:
                if (TryDeserialize<ChatTopicDto>(envelope.Payload, out var topicEvt))
                {
                    ApplyTopic(topicEvt);
                }
                return;
        }
    }

    private static bool TryDeserialize<T>(JsonElement element, out T result) where T : class
    {
        try
        {
            var r = element.Deserialize<T>();
            result = r!;
            return r is not null;
        }
        catch
        {
            result = null!;
            return false;
        }
    }

    private void ApplyEdit(ChatEditDto edit)
    {
        var msgRef = _recent.FirstOrDefault(r => r.MessageId == edit.MessageId);
        if (msgRef is null) return;
        msgRef.Body = edit.Body;
        msgRef.Edited = true;
        var newLine = RenderMessage(msgRef);
        _app.Invoke(() => _log.TryReplace(edit.MessageId, newLine));
    }

    private void ApplyDelete(ChatDeleteDto del)
    {
        var msgRef = _recent.FirstOrDefault(r => r.MessageId == del.MessageId);
        if (msgRef is null) return;
        msgRef.Deleted = true;
        var line = MessageRenderer.RenderDeleted(_user.FormatClock(msgRef.At), msgRef.Handle);
        _app.Invoke(() => _log.TryReplace(del.MessageId, line));
    }

    private void ApplyPin(ChatPinDto pin)
    {
        var msgRef = _recent.FirstOrDefault(r => r.MessageId == pin.MessageId);
        if (msgRef is null) return;
        msgRef.Pinned = pin.IsPinned;
        if (msgRef.Deleted) return; // tombstones don't change pin glyphs
        var line = RenderMessage(msgRef);
        _app.Invoke(() => _log.TryReplace(pin.MessageId, line));
    }

    private void ApplyTopic(ChatTopicDto evt)
    {
        if (evt.ChannelId != _currentChannel.Id) return;
        _currentChannel.Topic = evt.Topic;
        UpdateChrome();
        AppendSystem($"─ topic set by {evt.ActorHandle}: {evt.Topic ?? "(cleared)"}");
    }

    private void ApplyReaction(ChatReactionDto react, bool add)
    {
        if (!_reactions.TryGetValue(react.MessageId, out var map))
        {
            if (!add) return;
            map = new Dictionary<string, HashSet<long>>();
            _reactions[react.MessageId] = map;
        }
        if (!map.TryGetValue(react.Emoji, out var users))
        {
            if (!add) return;
            users = new HashSet<long>();
            map[react.Emoji] = users;
        }
        if (add) users.Add(react.UserId); else users.Remove(react.UserId);
        if (users.Count == 0) map.Remove(react.Emoji);
        if (map.Count == 0) _reactions.Remove(react.MessageId);

        PushReactionFooter(react.MessageId);
    }

    private void PushReactionFooter(long messageId)
    {
        var summaries = BuildSummaries(messageId);
        _app.Invoke(() => _log.TrySetReactions(messageId, summaries));
    }

    private IReadOnlyList<ReactionSummary> BuildSummaries(long messageId)
    {
        if (!_reactions.TryGetValue(messageId, out var map) || map.Count == 0)
            return Array.Empty<ReactionSummary>();
        return map.Select(kv => new ReactionSummary(kv.Key, kv.Value.Count, kv.Value.Contains(_user.Id)))
                  .OrderByDescending(s => s.Count)
                  .ThenBy(s => s.Emoji, StringComparer.Ordinal)
                  .ToArray();
    }

    private void SeedReactions(long messageId, IEnumerable<MessageReaction> rows)
    {
        var map = new Dictionary<string, HashSet<long>>();
        foreach (var r in rows)
        {
            if (!map.TryGetValue(r.Emoji, out var set))
            {
                set = new HashSet<long>();
                map[r.Emoji] = set;
            }
            set.Add(r.UserId);
        }
        if (map.Count > 0) _reactions[messageId] = map;
    }

    private async Task RunPresenceSubscribeAsync(long channelId, CancellationToken ct)
    {
        var bus = _services.GetRequiredService<IRealtimeBus>();
        try
        {
            await foreach (var payload in bus.SubscribeAsync(ChatTopics.Presence(channelId), ct))
            {
                PresenceEventDto? evt;
                try { evt = JsonSerializer.Deserialize<PresenceEventDto>(payload); }
                catch { continue; }
                if (evt is null) continue;
                if (evt.Kind == PresenceEventKind.Typing)
                {
                    NoteTyper(evt.Handle);
                    continue;
                }
                // Any non-typing presence ping triggers a refresh from authoritative state
                // in Redis — we don't trust the wire to be the source of truth.
                await RefreshSidebarAsync(ct);
            }
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
    }

    private async Task RunTypingPruneAsync(CancellationToken ct)
    {
        try
        {
            while (!ct.IsCancellationRequested)
            {
                await Task.Delay(TimeSpan.FromSeconds(1), ct);
                PruneTyperHints();
            }
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
    }

    private void NoteTyper(string handle)
    {
        if (string.IsNullOrEmpty(handle)) return;
        if (string.Equals(handle, _user.Handle, StringComparison.OrdinalIgnoreCase)) return;
        lock (_typers)
        {
            _typers[handle] = DateTimeOffset.UtcNow;
        }
        UpdateTypingHint();
    }

    private void ClearTyperOnUiThread(string handle)
    {
        lock (_typers) _typers.Remove(handle);
        UpdateTypingHint();
    }

    private void PruneTyperHints()
    {
        var cutoff = DateTimeOffset.UtcNow - TypingFade;
        bool changed = false;
        lock (_typers)
        {
            foreach (var key in _typers.Where(kv => kv.Value < cutoff).Select(kv => kv.Key).ToArray())
            {
                _typers.Remove(key);
                changed = true;
            }
        }
        if (changed) UpdateTypingHint();
    }

    // Recomputes the trailing "alice is typing…" / "alice, bob are typing…" hint and pokes
    // the status line to redraw. Status text is recomputed entirely here so a transient
    // typing burst doesn't paste onto whatever error/status was there before.
    private void UpdateTypingHint()
    {
        string[] names;
        lock (_typers) names = _typers.Keys.OrderBy(h => h, StringComparer.OrdinalIgnoreCase).ToArray();
        string hint;
        if (names.Length == 0) hint = string.Empty;
        else if (names.Length == 1) hint = $"{names[0]} is typing…";
        else if (names.Length == 2) hint = $"{names[0]} and {names[1]} are typing…";
        else if (names.Length == 3) hint = $"{names[0]}, {names[1]}, and {names[2]} are typing…";
        else hint = $"several people are typing…";

        if (hint == _typingHint) return;
        _typingHint = hint;
        RefreshStatusLine();
    }

    private void RefreshStatusLine()
    {
        var baseStatus = $"in {LabelFor(_currentChannel)}  topic: {_currentChannel.Topic ?? "(none)"}";
        var text = string.IsNullOrEmpty(_typingHint) ? baseStatus : $"{baseStatus}  |  {_typingHint}";
        _app.Invoke(() =>
        {
            _status.Text = text;
            _status.SetScheme(BbsTheme.Status);
        });
    }

    // Debounced typing publish. Fires at most once per TypingDebounce, only when the input
    // has content (no point announcing typing when the field is empty).
    private async Task MaybePublishTypingAsync()
    {
        try
        {
            if (string.IsNullOrEmpty(_input.Text)) return;
            var now = DateTimeOffset.UtcNow;
            if (now - _lastTypingPublishedAt < TypingDebounce) return;
            _lastTypingPublishedAt = now;
            var presence = _services.GetRequiredService<PresenceService>();
            await presence.PublishTypingAsync(_currentChannel.Id, _user.Id, _user.Handle, _shutdown.Token);
        }
        catch { /* typing is best-effort */ }
    }

    private async Task RunHeartbeatAsync(long channelId, CancellationToken ct)
    {
        var presence = _services.GetRequiredService<PresenceService>();
        try
        {
            while (!ct.IsCancellationRequested)
            {
                await Task.Delay(HeartbeatPeriod, ct);
                await presence.HeartbeatAsync(channelId, _user.Id, _user.Handle, ct);
                // Periodic refresh covers TTL-driven evictions that don't fire an event.
                if (DateTimeOffset.UtcNow.Ticks % 3 == 0) // ~every 3rd heartbeat
                {
                    await RefreshSidebarAsync(ct);
                }
            }
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
    }

    private async Task RunChannelsRefreshAsync(CancellationToken ct)
    {
        try
        {
            while (!ct.IsCancellationRequested)
            {
                await Task.Delay(ChannelRefreshPeriod, ct);
                await RefreshChannelsAsync(ct);
            }
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
    }

    private async Task RefreshChannelsAsync(CancellationToken ct)
    {
        try
        {
            var reads = _services.GetRequiredService<ReadStateService>();
            var entries = await reads.ListForUserAsync(_user.Id, ct);
            _channelEntries = entries;
            var rows = entries
                .Select((e, i) => FormatChannelRow(e, i, isCurrent: e.ChannelId == _currentChannel.Id))
                .ToArray();
            _app.Invoke(() =>
            {
                _channelsList.SetSource<string>(new ObservableCollection<string>(rows));
                _channelsPane.Title = entries.Count > 0 ? $"channels ({entries.Count})" : "channels";
            });
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
        catch { /* sidebar is best-effort */ }
    }

    private static string FormatChannelRow(ReadStateService.ChannelEntry e, int index, bool isCurrent)
    {
        var marker = isCurrent ? "▸" : " ";
        var slot = index < 9 ? (index + 1).ToString() : (index == 9 ? "0" : " ");
        var prefix = e.Name.StartsWith("dm-", StringComparison.Ordinal) ? "@" : "#";
        var label = e.Name.StartsWith("dm-", StringComparison.Ordinal)
            ? FormatDmLabel(e.Name)
            : e.Name;
        var badge = e.UnreadCount > 0 ? $" ({e.UnreadCount})" : "";
        return $"{marker}{slot} {prefix}{label}{badge}";
    }

    // Render a DM channel name (dm-{lo}-{hi}) as just the other participant's handle. We
    // can't easily derive *which* of the two is "the other" without the current handle in
    // scope, but the sidebar caller (ChatScreen) already filters by user, so showing both
    // is fine for v1.
    private static string FormatDmLabel(string dmName)
    {
        // dm-{a}-{b} → "a/b" — cheap and unambiguous.
        var rest = dmName.Substring(3);
        return rest;
    }

    private async Task MarkReadSafelyAsync(long channelId, long messageId)
    {
        try
        {
            var reads = _services.GetRequiredService<ReadStateService>();
            await reads.MarkReadAsync(_user.Id, channelId, messageId, _shutdown.Token);
        }
        catch { /* read-state is best-effort */ }
    }

    private async Task RefreshSidebarAsync(CancellationToken ct)
    {
        try
        {
            var presence = _services.GetRequiredService<PresenceService>();
            var members = await presence.ListAsync(_currentChannel.Id, ct);
            var ordered = members
                .OrderBy(h => string.Equals(h, _user.Handle, StringComparison.OrdinalIgnoreCase) ? 0 : 1)
                .ThenBy(h => h, StringComparer.OrdinalIgnoreCase)
                .ToArray();
            _app.Invoke(() =>
            {
                _sidebarList.SetSource<string>(new ObservableCollection<string>(ordered));
                _sidebar.Title = $"online ({ordered.Length})";
            });
        }
        catch (OperationCanceledException) { /* expected on close/switch */ }
        catch { /* presence is best-effort */ }
    }

    private async Task SendMessageAsync(string body)
    {
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
            var envelope = new ChatEnvelope(ChatEventKind.Message, JsonSerializer.SerializeToElement(dto));
            await bus.PublishAsync(
                ChatTopics.Channel(_currentChannel.Id),
                JsonSerializer.SerializeToUtf8Bytes(envelope),
                _shutdown.Token);
        }
        catch (OperationCanceledException) { /* expected on close */ }
        catch (Exception ex)
        {
            AppendSystem($"[!] send failed: {ex.Message}", isError: true);
        }
    }

    private void UpdateChrome()
    {
        var label = LabelFor(_currentChannel);
        Title = $"{label} — {_user.Handle} — /help — [Esc] back to lobby";
        SetStatus($"in {label}  topic: {_currentChannel.Topic ?? "(none)"}");
    }

    private static string LabelFor(Channel channel) =>
        channel.Name.StartsWith("dm-", StringComparison.Ordinal)
            ? "DM"
            : $"#{channel.Name}";

    private void SetStatus(string text) => _app.Invoke(() =>
    {
        _status.Text = text;
        _status.SetScheme(text.StartsWith("[!]") ? BbsTheme.Warning : BbsTheme.Status);
    });

    private void AppendSystem(string text, bool isError = false) =>
        AppendOnUiThread(MessageRenderer.RenderSystem(text, isError));

    private void AppendOnUiThread(ChatLine line, long? messageId = null)
    {
        _app.Invoke(() =>
        {
            _log.Append(line, messageId);
            if (line.SelfMentioned)
            {
                _status.Text = "@ you were mentioned";
                _status.SetScheme(BbsTheme.Warning);
            }
        });
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
        return MessageRenderer.RenderMessage(clock, msgRef.Handle, msgRef.Body ?? string.Empty, _user.Handle,
            edited: msgRef.Edited, pinned: msgRef.Pinned);
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            try { _shutdown.Cancel(); } catch { /* ignore */ }
            try { _channelCts.Cancel(); } catch { /* ignore */ }
            // Best-effort leave on the way out so other sessions see us drop immediately
            // instead of waiting for the Redis TTL to evict.
            try { _ = LeaveChannelPresenceAsync(_currentChannel.Id); } catch { /* ignore */ }
            _shutdown.Dispose();
            _channelCts.Dispose();
        }
        base.Dispose(disposing);
    }

    private static int? ChannelSlotForAltKey(Key key)
    {
        if (key == Key.D1.WithAlt) return 0;
        if (key == Key.D2.WithAlt) return 1;
        if (key == Key.D3.WithAlt) return 2;
        if (key == Key.D4.WithAlt) return 3;
        if (key == Key.D5.WithAlt) return 4;
        if (key == Key.D6.WithAlt) return 5;
        if (key == Key.D7.WithAlt) return 6;
        if (key == Key.D8.WithAlt) return 7;
        if (key == Key.D9.WithAlt) return 8;
        if (key == Key.D0.WithAlt) return 9;
        return null;
    }

    // Snapshot of an on-screen message that the position-based commands resolve against
    // and that envelope dispatchers update in place after edit/pin/delete events. Mutable
    // so we don't have to swap entries in _recent for every state change.
    private sealed class MessageRef
    {
        public required long MessageId { get; init; }
        public required string Handle { get; init; }
        public required DateTimeOffset At { get; init; }
        public required string Body { get; set; }
        public bool Edited { get; set; }
        public bool Pinned { get; set; }
        public bool Deleted { get; set; }
    }
}
