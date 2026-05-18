using System.Collections.Concurrent;
using System.Collections.ObjectModel;
using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Night.Ms.Imaging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui.Art;
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
    // Channel sidebar refresh cadence. 15s is the slowest that still feels live for "another
    // session in my account just sent a message in #foo and the unread badge needs to update";
    // tighter values (we used 5s previously) multiplied 1:1 with active chat sessions and made
    // ListForUserAsync the dominant DB workload at scale.
    private static readonly TimeSpan ChannelRefreshPeriod = TimeSpan.FromSeconds(15);

    // Image-render bounds for inline chat images. Cap is intentionally low so a big image
    // doesn't dominate the narrow chat column; floor stops tiny favicons from rendering
    // as 1×1 dots. Concurrency is 2 — bursty pastes don't need to saturate the network.
    private const int ImageRenderColsCap = 40;
    private const int ImageRenderColsFloor = 8;
    private const int ImageSourcePixelsPerCell = 10;
    private const int ImageFetchConcurrency = 2;
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
    private readonly ChatInputPreview _preview;
    private readonly BbsChatStatusLine _status;
    private readonly ConcurrentDictionary<long, string> _drafts = new();

    // Tracks the messages we've displayed, newest-first, so /react /edit /del <n> can map a
    // position to a real ChatMessage.Id. Also owns the per-message reaction map.
    private readonly ChatMessageLog _msgLog = new();
    private readonly ChatEnvelopeDispatcher _dispatcher;

    // Per-session image cache. Keyed by URL so repeated postings of the same link don't
    // re-fetch / re-render. Bounded indirectly by the per-session HttpImageFetcher cache,
    // which evicts decoded Image<Rgba32> after a fixed-size FIFO. Half-block rendering is
    // cheap so we don't bother LRU-ing CellGrids — just keep what we've drawn.
    private readonly ConcurrentDictionary<Uri, CellGrid> _imageGrids = new();
    private readonly SemaphoreSlim _imageFetchSemaphore = new(ImageFetchConcurrency, ImageFetchConcurrency);

    // Cache of "has this handle uploaded a profile picture?". Looked up lazily the first
    // time we render a message from a handle; once known, future messages from the same
    // handle in this session get a "●" prefix without re-querying. Stale state (e.g. user
    // uploads a pfp mid-session in another tab) corrects on next channel re-entry.
    private readonly ConcurrentDictionary<string, bool> _hasPfpByHandle = new(StringComparer.OrdinalIgnoreCase);

    // Active "typing…" hints — handle → last-seen-typing-at. Pruned each tick of _typingTimer.
    private readonly Dictionary<string, DateTimeOffset> _typers = new();
    private DateTimeOffset _lastTypingPublishedAt = DateTimeOffset.MinValue;
    private string _typingHint = string.Empty;

    // Coalesces "the right sidebar needs to be re-fetched from Redis." Set by the presence
    // subscriber whenever a non-typing event arrives; cleared by the heartbeat loop after a
    // single refresh. Without this, every join/leave broadcast triggered a Redis ZRANGE in
    // every receiver (O(N²) per channel — a #lobby with 500 users + one new joiner would
    // produce 500 ZRANGE calls in a burst). 0 = clean, 1 = dirty.
    private int _sidebarDirty;
    private int _heartbeatTick;

    // Current sidebar contents. _channelEntries lives at Screen scope (not per-channel) so
    // Alt+digit can switch without re-querying. Rebuilt by RefreshChannelsAsync.
    private IReadOnlyList<ReadStateService.ChannelEntry> _channelEntries = Array.Empty<ReadStateService.ChannelEntry>();
    // Highest message id seen in the current channel, used to bump the read pointer.
    private long _lastReadMessageId;

    private Channel _currentChannel;
    private CancellationTokenSource _channelCts = new();
    // Linked CTS combining _shutdown and _channelCts. Recreated per LoadHistoryAndSubscribeAsync;
    // disposed by TeardownChannelTasksAsync so a long-running session doesn't leak one CTS per /join.
    private CancellationTokenSource? _channelLinkedCts;
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

        // Subscribe to the channel topic and route each envelope through screen-side
        // handlers. The dispatcher owns deserialization + the type switch; OnMessage et al.
        // hold the channel-specific glue (image fetches, mark-read, reply-count badges).
        _dispatcher = new ChatEnvelopeDispatcher
        {
            OnMessage = OnMessageEvent,
            OnEdit = OnEditEvent,
            OnDelete = OnDeleteEvent,
            OnPin = OnPinEvent,
            OnReaction = OnReactionEvent,
            OnTopic = OnTopicEvent,
        };

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

        _status = new BbsChatStatusLine
        {
            X = 0,
            Y = Pos.Bottom(_log),
            Width = Dim.Fill(),
            Height = 1,
        };

        // One-row preview shown above the input while the buffer is non-empty. Starts hidden
        // with Height=0 so it doesn't reserve a row at chat-empty rest.
        _preview = new ChatInputPreview
        {
            X = 0,
            Y = Pos.Bottom(_status),
            Width = Dim.Fill(),
            Height = 0,
            Visible = false,
        };

        _input = new TextField
        {
            X = 0,
            Y = Pos.Bottom(_preview),
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
                    UpdatePreview();
                    HandleInputAsync(text).FireAndLog(_services, nameof(HandleInputAsync));
                }
                return;
            }
            if (key == Key.PageUp)        { key.Handled = true; _log.ScrollPage(-1); return; }
            if (key == Key.PageDown)      { key.Handled = true; _log.ScrollPage(+1); return; }
            if (key == Key.Home.WithCtrl) { key.Handled = true; _log.ScrollToTop(); return; }
            if (key == Key.End.WithCtrl)  { key.Handled = true; _log.ScrollToBottom(); return; }

            // Every non-scroll, non-Enter keystroke is a typing signal. Debounced inside
            // MaybePublishTypingAsync so we don't fan out 1 event per character.
            MaybePublishTypingAsync().FireAndLog(_services, nameof(MaybePublishTypingAsync));
            // Defer so TextField has already applied the keystroke when we re-read Text.
            _app.Invoke(UpdatePreview);
        };

        Add(_channelsPane, _log, _sidebar, _status, _preview, _input);
        _input.SetFocus();

        InstallEscapeHandler(() => ShutdownCts.Cancel());
        KeyDown += (_, key) =>
        {
            // Alt+1..Alt+9 jumps to the Nth channel in the sidebar; Alt+0 is the 10th slot.
            // Alt+digit is universal across PuTTY/WT/iTerm; plain digits would conflict with
            // input typing.
            var slot = ChannelSlotForAltKey(key);
            if (slot is not null)
            {
                key.Handled = true;
                SwitchByIndexAsync(slot.Value).FireAndLog(_services, nameof(SwitchByIndexAsync));
            }
        };

        UpdateChrome();
        LoadHistoryAndSubscribeAsync().FireAndLog(_services, nameof(LoadHistoryAndSubscribeAsync));
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
                AppendSystem(SlashCommands.HelpText);
                return;

            case "/quit":
            case "/exit":
                ShutdownCts.Cancel();
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

            case "/reply":
                await ReplyAsync(arg);
                return;

            case "/thread":
                OpenThread(arg);
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
            var target = await db.Channels.AsNoTracking().FirstOrDefaultAsync(c => c.Id == entry.ChannelId, Shutdown);
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
        var result = await chat.JoinPublicChannelAsync(channelName, _user.Id, Shutdown);
        await ApplyJoinResultAsync(result);
    }

    private async Task SwitchToDmAsync(string handle)
    {
        var chat = _services.GetRequiredService<ChatService>();
        var result = await chat.JoinDmAsync(_user, handle, Shutdown);
        await ApplyJoinResultAsync(result);
    }

    private async Task FingerAsync(string handle)
    {
        try
        {
            var profile = _services.GetRequiredService<ProfileService>();
            var snap = await profile.GetByHandleAsync(handle.Trim(), Shutdown);
            if (snap is null)
            {
                AppendSystem($"── finger {handle} ──\n   no such user.");
                return;
            }
            // Open the modal FingerScreen instead of dumping text into chat scrollback: this
            // gives us room for the half-block avatar render alongside the text fields.
            // Application.Run blocks until the user presses Esc, so we marshal back to the UI
            // thread and let it block there.
            _app.Invoke(() =>
            {
                _app.Run(new FingerScreen(_app, _services, _user, snap));
            });
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
            var members = await presence.ListAsync(_currentChannel.Id, Shutdown);
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
        var result = await muts.EditAsync(msgRef.MessageId, _user.Id, _user.IsSysop, newBody, Shutdown);
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
        var result = await muts.DeleteAsync(msgRef.MessageId, _user.Id, _user.IsSysop, Shutdown);
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
            ? await muts.PinAsync(msgRef.MessageId, _user.Id, Shutdown)
            : await muts.UnpinAsync(msgRef.MessageId, _user.Id, Shutdown);
        ReportMutation(result);
    }

    private async Task ListPinsAsync()
    {
        try
        {
            var muts = _services.GetRequiredService<ChatMutationService>();
            var pins = await muts.ListPinsAsync(_currentChannel.Id, Shutdown);
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
        var result = await muts.SetTopicAsync(_currentChannel.Id, _user.Id, _user.IsSysop, _user.Handle, arg, Shutdown);
        ReportMutation(result);
    }

    // /thread <n> — opens a focused view of the n-th most recent message + all its replies.
    // Runs as a nested app.Run (same pattern as NewsScreen → ReaderScreen); when the user
    // hits Esc, control returns here with our channel subscription intact. Background tasks
    // here stay live during the nested loop — incoming events still mutate _msgLog so when
    // we redraw on return, state is current.
    private void OpenThread(string? arg)
    {
        if (string.IsNullOrEmpty(arg) || !int.TryParse(arg, out var pos))
        {
            SetStatus("[!] usage: /thread <n>");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        // A reply opens the thread of its parent — viewing a reply as a "root" would hide
        // its siblings. Conceptually a thread is keyed by the original message.
        var rootId = msgRef.ParentMessageId ?? msgRef.MessageId;
        _app.Run(new ChatThreadScreen(_services, _app, _user, _currentChannel.Id, rootId));
        // After the inner screen closes, restore our chrome — the nested screen overwrote
        // Title and the status line.
        UpdateChrome();
    }

    // /reply <n> <body> — sends `body` as a threaded reply to the n-th most recent message.
    private async Task ReplyAsync(string? arg)
    {
        if (string.IsNullOrEmpty(arg) || !TryParsePositionArg(arg, out var pos, out var body) || string.IsNullOrWhiteSpace(body))
        {
            SetStatus("[!] usage: /reply <n> <body>");
            return;
        }
        if (!TryResolveMessage(pos, out var msgRef))
        {
            SetStatus($"[!] no message at position {pos}.");
            return;
        }
        await SendMessageAsync(body, parentMessageId: msgRef.MessageId);
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
            var hits = await muts.SearchAsync(_currentChannel.Id, arg, limit: 50, Shutdown);
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
        if (idx < 0 || idx >= _msgLog.Count) return false;
        msgRef = _msgLog.Messages[idx];
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
        _app.Invoke(() => { _input.Text = string.Empty; UpdatePreview(); });

        // Let the prior channel's presence/message/heartbeat tasks know we're moving.
        await TeardownChannelTasksAsync();

        // Inform the prior channel we left, then move state.
        await LeaveChannelPresenceAsync(_currentChannel.Id);
        _channelCts.Dispose();
        _channelCts = new CancellationTokenSource();

        _currentChannel = target;
        _msgLog.Clear();
        var label = LabelFor(target);
        _app.Invoke(() =>
        {
            _log.Clear();
            _log.Append(MessageRenderer.RenderSystem($"--- {verb} {label} ---"));
            if (_drafts.TryGetValue(target.Id, out var draft)) _input.Text = draft;
            UpdatePreview();
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
        _channelLinkedCts?.Dispose();
        _channelLinkedCts = null;
        lock (_typers) _typers.Clear();
        _typingHint = string.Empty;
    }

    private async Task LeaveChannelPresenceAsync(long channelId)
    {
        try
        {
            var presence = _services.GetRequiredService<PresenceService>();
            await presence.LeaveAsync(channelId, _user.Id, _user.Handle, Shutdown);
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
            var ids = history.Select(m => m.Id).ToArray();
            var reactionMap = await muts.SnapshotReactionsAsync(ids, _channelCts.Token);
            // Reply counts per parent in the loaded window. Children whose parents have
            // already scrolled off don't contribute (we can't render a badge for an
            // off-screen parent anyway).
            var replyCounts = await muts.SnapshotReplyCountsAsync(ids, _channelCts.Token);

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
                    ParentMessageId = msg.ParentMessageId,
                    ReplyCount = replyCounts.GetValueOrDefault(msg.Id),
                };
                _msgLog.Insert(0, msgRef);
                AppendOnUiThread(RenderMessage(msgRef), msg.Id);
                if (reactionMap.TryGetValue(msg.Id, out var rows))
                {
                    _msgLog.SeedReactions(msg.Id, rows);
                    PushReactionFooter(msg.Id);
                }
                if (!msgRef.Deleted) ScheduleImageFetches(msg.Id, msg.Body);
                if (msg.Id > highestSeenId) highestSeenId = msg.Id;
            }
            _lastReadMessageId = highestSeenId;
            // Mark the channel read at the highest id we just displayed. Done in the
            // background so a slow DB doesn't delay the first render of the chat.
            if (highestSeenId > 0)
            {
                MarkReadSafelyAsync(_currentChannel.Id, highestSeenId).FireAndLog(_services, nameof(MarkReadSafelyAsync));
            }

            _channelLinkedCts = CancellationTokenSource.CreateLinkedTokenSource(Shutdown, _channelCts.Token);
            var channelToken = _channelLinkedCts.Token;
            _subscriber = Task.Run(() => RunSubscribeAsync(_currentChannel.Id, channelToken));
            _presenceSubscriber = Task.Run(() => RunPresenceSubscribeAsync(_currentChannel.Id, channelToken));
            _heartbeat = Task.Run(() => RunHeartbeatAsync(_currentChannel.Id, channelToken));
            _typingPrune = Task.Run(() => RunTypingPruneAsync(channelToken));
            _channelsRefresh = Task.Run(() => RunChannelsRefreshAsync(channelToken));

            // Announce ourselves into the channel's presence set, then refresh both
            // sidebars from authoritative state.
            var presence = _services.GetRequiredService<PresenceService>();
            await presence.JoinAsync(_currentChannel.Id, _user.Id, _user.Handle, Shutdown);
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

    private void DispatchChatEnvelope(byte[] payload) => _dispatcher.Dispatch(payload);

    private void OnMessageEvent(ChatMessageDto msg)
    {
        var newRef = new MessageRef
        {
            MessageId = msg.Id,
            Handle = msg.Handle,
            At = msg.CreatedAt,
            Body = msg.Body,
            ParentMessageId = msg.ParentMessageId,
        };
        _msgLog.Insert(0, newRef);
        AppendOnUiThread(RenderMessage(newRef), msg.Id);
        ScheduleImageFetches(msg.Id, msg.Body);
        // If this message is a reply to one still on screen, bump that parent's reply
        // count and re-render its line so the "[N replies]" badge updates. Quietly no-ops
        // when the parent has scrolled past the on-screen window — the count will be
        // correct on next channel re-entry because LoadHistoryAndSubscribeAsync rehydrates.
        var parent = _msgLog.BumpReplyCount(msg.ParentMessageId);
        if (parent is not null)
        {
            var parentLine = RenderMessage(parent);
            _app.Invoke(() => _log.TryReplace(parent.MessageId, parentLine));
        }
        // The sender is implicitly no longer typing — clear their hint so the status bar
        // doesn't say "alice is typing…" right after alice posts.
        ClearTyperOnUiThread(msg.Handle);
        // We're looking at the channel — bump the read pointer so the unread badge stays
        // at zero. Fire-and-forget to keep the envelope dispatcher synchronous; failures
        // are surfaced by the next refresh.
        if (msg.Id > _lastReadMessageId)
        {
            _lastReadMessageId = msg.Id;
            MarkReadSafelyAsync(_currentChannel.Id, msg.Id).FireAndLog(_services, nameof(MarkReadSafelyAsync));
        }
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
        _app.Invoke(() =>
        {
            _log.TryReplace(del.MessageId, line);
            _log.TryClearImages(del.MessageId);
        });
    }

    private void OnPinEvent(ChatPinDto pin)
    {
        var msgRef = _msgLog.ApplyPin(pin);
        if (msgRef is null || msgRef.Deleted) return; // tombstones don't change pin glyphs
        var line = RenderMessage(msgRef);
        _app.Invoke(() => _log.TryReplace(pin.MessageId, line));
    }

    private void OnTopicEvent(ChatTopicDto evt)
    {
        if (evt.ChannelId != _currentChannel.Id) return;
        _currentChannel.Topic = evt.Topic;
        UpdateChrome();
        AppendSystem($"─ topic set by {evt.ActorHandle}: {evt.Topic ?? "(cleared)"}");
    }

    private void OnReactionEvent(ChatReactionDto react, bool add)
    {
        _msgLog.ApplyReaction(react, add);
        PushReactionFooter(react.MessageId);
    }

    private void PushReactionFooter(long messageId)
    {
        var summaries = _msgLog.BuildSummaries(messageId, _user.Id);
        _app.Invoke(() => _log.TrySetReactions(messageId, summaries));
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
                // Mark the sidebar dirty; the heartbeat tick is responsible for actually
                // fetching from Redis. Per-event refresh used to fan out one ZRANGE call to
                // every member of the channel for every join/leave — coalescing pushes that
                // to one Redis call per receiver per heartbeat regardless of event rate.
                Volatile.Write(ref _sidebarDirty, 1);
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
        var line = BuildTopicStatusLine();
        _app.Invoke(() => _status.SetStyled(line));
    }

    // Builds the styled "in #lobby  topic: <topic>  |  alice is typing..." line. The topic
    // portion runs through MessageRenderer.PreviewBody so *bold*/_italic_/`code`/:emoji:
    // render the same as in chat bodies. Chrome (prefix/separator/typing hint) uses dim
    // ChatPalette.Chrome to match the timestamp + "[N replies]" chrome elsewhere.
    private ChatLine BuildTopicStatusLine()
    {
        var runs = new List<ChatRun>(8);
        runs.Add(new ChatRun($"in {LabelFor(_currentChannel)}  topic: ", ChatPalette.Chrome, ArtStyle.None));
        var topic = _currentChannel.Topic;
        if (string.IsNullOrEmpty(topic))
        {
            runs.Add(new ChatRun("(none)", ChatPalette.Chrome, ArtStyle.Italic));
        }
        else
        {
            var topicLine = MessageRenderer.PreviewBody(topic, _user.Handle);
            foreach (var run in topicLine.Runs) runs.Add(run);
        }
        if (!string.IsNullOrEmpty(_typingHint))
        {
            runs.Add(new ChatRun($"  |  {_typingHint}", ChatPalette.Chrome, ArtStyle.Italic));
        }
        return new ChatLine(runs);
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
            await presence.PublishTypingAsync(_currentChannel.Id, _user.Id, _user.Handle, Shutdown);
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

                // Refresh the right sidebar from Redis at most once per heartbeat. Two
                // triggers: (a) presence subscriber set the dirty flag in response to a
                // join/leave broadcast since the last tick; (b) rescue refresh every 3rd
                // tick (~30s) to catch presence rows that aged out via TTL without firing
                // an explicit event. Either path consumes the dirty flag.
                var tick = ++_heartbeatTick;
                var dirty = Interlocked.Exchange(ref _sidebarDirty, 0) == 1;
                if (dirty || tick % 3 == 0)
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

    // Scans the message body for image URLs and kicks off a background fetch+render for
    // each one. Cached grids (same URL already seen this session) paint immediately on the
    // UI thread; new fetches go through the per-screen semaphore. Failures are silent —
    // the link text remains visible in the body either way.
    private void ScheduleImageFetches(long messageId, string body)
    {
        var urls = UrlExtractor.FindImageUrls(body);
        if (urls.Count == 0) return;
        foreach (var url in urls)
        {
            if (_imageGrids.TryGetValue(url, out var cached))
            {
                AttachImageOnUiThread(messageId, cached);
                continue;
            }
            Task.Run(() => FetchAndAttachAsync(messageId, url, Shutdown))
                .FireAndLog(_services, nameof(FetchAndAttachAsync));
        }
    }

    private async Task FetchAndAttachAsync(long messageId, Uri url, CancellationToken ct)
    {
        IImageFetcher fetcher;
        try
        {
            fetcher = _services.GetRequiredService<IImageFetcher>();
        }
        catch
        {
            return; // No fetcher registered in this build — leave the link as text.
        }

        await _imageFetchSemaphore.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            var image = await fetcher.FetchAsync(url, ct).ConfigureAwait(false);
            if (image is null || ct.IsCancellationRequested) return;

            // String-roundtrip via SgrParser, same pattern as ReaderScreen — the renderer
            // emits SGR + half-block "▀" and the parser folds that back into a CellGrid
            // our view can paint. Keeps the imaging library decoupled from CellGrid.
            var cols = Math.Clamp(image.Width / ImageSourcePixelsPerCell, ImageRenderColsFloor, ImageRenderColsCap);
            var ansi = HalfBlockRenderer.Render(image, cols, ColorDepth.Truecolor, DitherMode.None);
            var grid = SgrParser.Parse(ansi);
            _imageGrids[url] = grid;
            AttachImageOnUiThread(messageId, grid);
        }
        catch (OperationCanceledException) { /* screen closed mid-fetch */ }
        catch { /* fetcher already logs; leave link as text */ }
        finally
        {
            _imageFetchSemaphore.Release();
        }
    }

    private void AttachImageOnUiThread(long messageId, CellGrid grid)
    {
        _app.Invoke(() => _log.TryAddImage(messageId, grid));
    }

    private async Task MarkReadSafelyAsync(long channelId, long messageId)
    {
        try
        {
            var reads = _services.GetRequiredService<ReadStateService>();
            await reads.MarkReadAsync(_user.Id, channelId, messageId, Shutdown);
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

    private async Task SendMessageAsync(string body, long? parentMessageId = null)
    {
        try
        {
            var muts = _services.GetRequiredService<ChatMutationService>();
            var result = await muts.PostAsync(_currentChannel.Id, _user.Id, _user.Handle, body, parentMessageId, Shutdown);
            switch (result)
            {
                case ChatOpResult.Forbidden f: SetStatus($"[!] {f.Reason}"); break;
                case ChatOpResult.Invalid i:   SetStatus($"[!] {i.Reason}"); break;
                case ChatOpResult.NotFound:    SetStatus("[!] Channel no longer exists."); break;
            }
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
        RefreshStatusLine();
    }

    private static string LabelFor(Channel channel) =>
        channel.Name.StartsWith("dm-", StringComparison.Ordinal)
            ? "DM"
            : $"#{channel.Name}";

    private void SetStatus(string text) => _app.Invoke(() => _status.Set(text));

    // Recomputes the preview row from the current buffer. Toggles the row's Height/Visible
    // (and the log's Fill margin) so the row collapses entirely when the buffer is empty.
    // Cheap — regex pass on a short string per keystroke.
    private void UpdatePreview()
    {
        var text = _input.Text ?? string.Empty;
        if (string.IsNullOrEmpty(text))
        {
            if (_preview.Visible)
            {
                _preview.SetLine(null);
                _preview.Visible = false;
                _preview.Height = 0;
                _log.Height = Dim.Fill(3);
                SetNeedsLayout();
            }
            return;
        }

        var line = text.StartsWith("/", StringComparison.Ordinal)
            ? CommandHighlighter.Highlight(text, _user.Handle)
            : MessageRenderer.PreviewBody(text, _user.Handle);
        _preview.SetLine(line);
        if (!_preview.Visible)
        {
            _preview.Visible = true;
            _preview.Height = 1;
            _log.Height = Dim.Fill(4);
            SetNeedsLayout();
        }
    }

    private void AppendSystem(string text, bool isError = false) =>
        AppendOnUiThread(MessageRenderer.RenderSystem(text, isError));

    private void AppendOnUiThread(ChatLine line, long? messageId = null)
    {
        _app.Invoke(() =>
        {
            _log.Append(line, messageId);
            if (line.SelfMentioned)
            {
                _status.SetWarning("@ you were mentioned");
            }
        });
    }

    private ChatLine RenderMessage(MessageRef msgRef)
    {
        var clock = _user.FormatClock(msgRef.At);
        var hasPfp = HasPfp(msgRef.Handle);
        if (msgRef.Deleted)
        {
            return MessageRenderer.RenderDeleted(clock, msgRef.Handle);
        }
        if (msgRef.Body is not null && msgRef.Body.StartsWith("/me ", StringComparison.Ordinal))
        {
            return MessageRenderer.RenderEmote(clock, msgRef.Handle, msgRef.Body[4..], _user.Handle, hasPfp: hasPfp);
        }
        // Resolve the parent (if any) to its handle so we can render "↳ @alice" — only
        // works when the parent is still in the on-screen window. If the parent has
        // scrolled off the load, fall back to a generic "↳ @(earlier)" stub.
        string? replyToHandle = null;
        if (msgRef.ParentMessageId is { } pid)
        {
            var parent = _msgLog.Find(pid);
            replyToHandle = parent?.Handle ?? "(earlier)";
        }
        return MessageRenderer.RenderMessage(clock, msgRef.Handle, msgRef.Body ?? string.Empty, _user.Handle,
            edited: msgRef.Edited, pinned: msgRef.Pinned,
            replyToHandle: replyToHandle, replyCount: msgRef.ReplyCount,
            hasPfp: hasPfp);
    }

    // Returns whether the named handle has a profile picture, looking it up lazily on first
    // sighting. Unknown handles default to false (no marker); a background task fills in the
    // dictionary so the NEXT message from the same handle in this session gets the marker.
    private bool HasPfp(string handle)
    {
        if (_hasPfpByHandle.TryGetValue(handle, out var v)) return v;
        // Kick off a background lookup. Fire-and-forget; we don't await it because we don't
        // want to re-render historical messages, just mark future ones.
        Task.Run(async () =>
        {
            try
            {
                var profile = _services.GetRequiredService<ProfileService>();
                var snap = await profile.GetByHandleAsync(handle, Shutdown);
                _hasPfpByHandle[handle] = snap?.ProfilePictureUpdatedAt is not null;
            }
            catch (OperationCanceledException) { }
            catch
            {
                _hasPfpByHandle[handle] = false;
            }
        });
        return false;
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            try { _channelCts.Cancel(); } catch { /* ignore */ }
            // Best-effort leave on the way out so other sessions see us drop immediately
            // instead of waiting for the Redis TTL to evict.
            LeaveChannelPresenceAsync(_currentChannel.Id).FireAndLog(_services, nameof(LeaveChannelPresenceAsync));
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

}
