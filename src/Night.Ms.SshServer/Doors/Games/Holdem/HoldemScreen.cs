using System.Text;
using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Doors.Games.Holdem.Chat;
using Night.Ms.SshServer.Doors.Games.Holdem.Events;
using Night.Ms.SshServer.Doors.Multiplayer;
using Night.Ms.SshServer.Realtime;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Functional-minimum v1 screen: text-layout table, seated-mode only. The custom 80×16
// painted view, spectator mode, table chat pane, and reattach-resume flow are deferred to
// follow-ups so this chunk stays scoped.
//
// Flow on entry:
//   1. GetDefaultTableAsync → handle (chat channel, blinds, max seats)
//   2. Ask "Sit down for MinBet chips? [Y]/[N]"
//   3. SitDownAsync → coordinator publishes SeatChanged
//   4. Background subscriber tasks deliver public + private events; each handler
//      marshals view updates via _app.Invoke
internal sealed class HoldemScreen : BbsWindow
{
    private readonly IApplication _app;
    private readonly IServiceProvider _services;
    private readonly User _user;
    private readonly IPokerClient _client;
    private readonly IRealtimeBus _bus;
    private readonly ILogger<HoldemScreen> _log;

    private readonly Label _statusLabel;
    private readonly HoldemTableView _tableView;
    private readonly Label _actionLabel;
    private readonly TextField _amountInput;     // hidden until B/R; replaces action prompt during entry
    private readonly Label _amountHint;          // shows min/max range while entering
    private TableChatPane? _chatPane;            // attached once we have a chatChannelId

    private enum AmountMode { None, Bet, Raise }
    private AmountMode _amountMode = AmountMode.None;

    private enum Phase { Connecting, AwaitingChoice, SittingDown, Seated, Watching, Exiting }
    private Phase _phase = Phase.Connecting;

    private TableHandle? _handle;
    private bool _seated;
    private int _mySeat = -1;
    private string? _myHole1;
    private string? _myHole2;

    // Locked when reading or writing _latest. Events arrive on background subscriber
    // tasks; UI thread reads via _app.Invoke. Locked sections are tiny (record reads).
    private readonly object _stateLock = new();
    private TableSnapshot _latest = TableSnapshot.Empty;

    private CancellationTokenSource? _subsCts;
    private Task? _eventsTask;
    private Task? _privateTask;
    private Task? _heartbeatTask;

    private bool _busy;

    public HoldemScreen(IApplication app, IServiceProvider services, User user)
        : base(app, services, user)
    {
        _app = app;
        _services = services;
        _user = user;
        _client = services.GetRequiredService<IPokerClient>();
        _bus = services.GetRequiredService<IRealtimeBus>();
        _log = services.GetRequiredService<ILoggerFactory>().CreateLogger<HoldemScreen>();

        Title = $"ssh.night.ms — hold'em — {user.Handle}";

        var header = new Label { X = 2, Y = 0, Width = Dim.Fill(2), Text = "No-Limit Texas Hold'em — 5/10 blinds — 6 seats — CPUs keep the table warm" };
        header.SetScheme(BbsTheme.Header_);

        _statusLabel = new Label { X = 2, Y = 1, Width = Dim.Fill(2), Text = "Connecting…" };
        _statusLabel.SetScheme(BbsTheme.Hint);

        // Fixed-size custom painted view, centered horizontally. Chat pane (8 rows)
        // sits below; action prompt + status bar are at the bottom anchor.
        _tableView = new HoldemTableView
        {
            X = Pos.Center(),
            Y = 3,
        };

        _actionLabel = new Label { X = 2, Y = Pos.AnchorEnd(2), Width = Dim.Fill(2), Text = "[Esc] back" };
        _actionLabel.SetScheme(BbsTheme.Hint);

        // Hidden by default; revealed when the user presses B or R. Sized to overlay the
        // action prompt area so the screen layout stays still during entry.
        _amountHint = new Label { X = 2, Y = Pos.AnchorEnd(3), Width = Dim.Fill(2), Visible = false };
        _amountHint.SetScheme(BbsTheme.Hint);
        _amountInput = new TextField { X = 2, Y = Pos.AnchorEnd(2), Width = Dim.Fill(2), Visible = false };
        _amountInput.KeyDown += OnAmountKey;

        Add(header, _statusLabel, _tableView, _actionLabel, _amountHint, _amountInput);

        KeyDown += OnKey;
        InitializeAsync().FireAndLog(services, nameof(InitializeAsync));
    }

    // -- Initialization ---------------------------------------------------------------

    // Two-stage init keeps modal-on-background-thread bugs out of the picture:
    //   1. Fetch the table handle off-thread, then update the prompt label.
    //   2. User presses [Y] (UI thread) → kick off SitDownAsync, also off-thread.
    // The UI thread is never blocked on a synchronous modal; status flows through labels.
    private async Task InitializeAsync()
    {
        try
        {
            _handle = await _client.GetDefaultTableAsync(Shutdown);

            // Chat pane attaches as soon as we know the channel id — works whether the
            // user ends up seated or backs out at the prompt. Listeners + UI redraws are
            // marshaled through Application.Invoke by the pane itself.
            _app.Invoke(() =>
            {
                _chatPane = new TableChatPane(_app, _services, _user, _handle.ChatChannelId)
                {
                    X = 2,
                    Y = Pos.AnchorEnd(10),
                    Width = Dim.Fill(2),
                    Height = 8,
                };
                Add(_chatPane);
            });

            // Reattach: if this user is already seated (e.g. SSH dropped + reconnect from
            // a new session), skip the buy-in modal and go straight to seated mode with
            // the existing chip stack preserved.
            var existing = await _client.FindExistingSeatAsync(_user.Id, Shutdown);
            if (existing is not null)
            {
                _seated = true;
                _mySeat = existing.SeatIndex;
                await RefreshSnapshotAsync(Shutdown);
                StartSubscribers(privateToo: true);
                _phase = Phase.Seated;
                _app.Invoke(() =>
                {
                    _statusLabel.Text = $"Resumed seat {existing.SeatIndex + 1} at table {existing.TableId} with ${existing.StartingChips} in chips";
                    RenderAll();
                });
                return;
            }

            _phase = Phase.AwaitingChoice;
            _app.Invoke(() =>
            {
                _statusLabel.Text = $"Table {_handle.TableId} — buy-in {_handle.MinBuyIn} chips";
                _actionLabel.Text = "[S] sit down   [W] watch only   [Esc] back to doors";
            });
        }
        catch (OperationCanceledException) { }
        catch (Exception ex)
        {
            _log.LogError(ex, "Hold'em init: failed to fetch table");
            _app.Invoke(() =>
            {
                _statusLabel.Text = $"Failed to connect: {ex.Message} — press [Esc]";
                _phase = Phase.Exiting;
            });
        }
    }

    private async Task BeginWatchAsync()
    {
        // Spectator path: skip buy-in entirely. The chat pane is already attached so
        // table chat works; the public-events subscription drives table-state updates
        // just like a seated session. The private subscription is NOT started — viewers
        // don't get hole cards.
        if (_handle is null) return;
        _phase = Phase.SittingDown; // reuse "transition" guard
        _app.Invoke(() => _statusLabel.Text = "Watching…");
        try
        {
            await RefreshSnapshotAsync(Shutdown);
            StartSubscribers(privateToo: false);
            _phase = Phase.Watching;
            _app.Invoke(RenderAll);
        }
        catch (OperationCanceledException) { }
        catch (Exception ex)
        {
            _log.LogError(ex, "begin watch failed");
            _app.Invoke(() =>
            {
                _statusLabel.Text = $"Watch failed: {ex.Message}";
                _phase = Phase.Exiting;
            });
        }
    }

    private async Task BeginSitDownAsync()
    {
        if (_handle is null) return;
        _phase = Phase.SittingDown;
        _app.Invoke(() => _statusLabel.Text = "Buying in…");
        try
        {
            var result = await _client.SitDownAsync(
                _handle.TableId, _user.Id, _user.Handle, _handle.MinBuyIn, preferredSeat: null, Shutdown);
            switch (result)
            {
                case MultiplayerOpResult.Ok:
                case MultiplayerOpResult.AlreadySeated:
                    _seated = true;
                    break;
                case MultiplayerOpResult.InsufficientChips ic:
                    _app.Invoke(() =>
                    {
                        _statusLabel.Text = $"Not enough coins: need {ic.Need}, have {ic.Have}. Press [Esc] to back out.";
                        _actionLabel.Text = "[Esc] back to doors";
                        _phase = Phase.Exiting;
                    });
                    return;
                case MultiplayerOpResult.SeatFull:
                    _app.Invoke(() =>
                    {
                        _statusLabel.Text = "Table is full. Press [Esc] to back out.";
                        _actionLabel.Text = "[Esc] back to doors";
                        _phase = Phase.Exiting;
                    });
                    return;
                case MultiplayerOpResult.Rejected r:
                    _app.Invoke(() =>
                    {
                        _statusLabel.Text = $"Rejected: {r.Reason}. Press [Esc].";
                        _actionLabel.Text = "[Esc] back to doors";
                        _phase = Phase.Exiting;
                    });
                    return;
            }

            await RefreshSnapshotAsync(Shutdown);
            StartSubscribers(privateToo: true);
            _phase = Phase.Seated;
            UiInvoke(RenderAll);
        }
        catch (OperationCanceledException) { }
        catch (Exception ex)
        {
            _log.LogError(ex, "sit-down failed");
            _app.Invoke(() =>
            {
                _statusLabel.Text = $"Sit-down failed: {ex.Message}. Press [Esc].";
                _phase = Phase.Exiting;
            });
        }
    }

    private async Task RefreshSnapshotAsync(CancellationToken ct)
    {
        if (_handle is null) return;
        using var doc = await _client.GetSnapshotAsync(_handle.TableId, _user.Id, ct);
        var dto = doc.RootElement.Deserialize<SnapshotResyncDto>();
        if (dto is null) return;
        ApplySnapshot(dto);
    }

    private void StartSubscribers(bool privateToo)
    {
        if (_handle is null) return;
        _subsCts = CancellationTokenSource.CreateLinkedTokenSource(Shutdown);
        _eventsTask = Task.Run(() => SubscribeAsync(MultiplayerTopics.Events(_handle.GameKey, _handle.TableId), PublicDispatcher(), _subsCts.Token));
        // Spectators don't subscribe to the private topic — no hole cards to deliver to a
        // non-seated viewer, so we save a Redis subscription per spectator session.
        // Heartbeat is also seated-only: spectators can't be cashed out by abandonment.
        if (privateToo)
        {
            _privateTask = Task.Run(() => SubscribeAsync(MultiplayerTopics.Private(_handle.GameKey, _handle.TableId, _user.Id), PrivateDispatcher(), _subsCts.Token));
            _heartbeatTask = Task.Run(() => RunHeartbeatAsync(_subsCts.Token));
        }
    }

    private async Task RunHeartbeatAsync(CancellationToken ct)
    {
        // 10s cadence matches the existing PresenceService pattern in this codebase and
        // gives the coordinator's 5-min abandoned-seat sweep ~30 missed heartbeats before
        // cashing the player out.
        var timer = new PeriodicTimer(TimeSpan.FromSeconds(10));
        try
        {
            while (await timer.WaitForNextTickAsync(ct))
            {
                if (_handle is null) continue;
                try { await _client.HeartbeatAsync(_handle.TableId, _user.Id, ct); }
                catch (OperationCanceledException) { return; }
                catch (Exception ex) { _log.LogWarning(ex, "heartbeat failed"); }
            }
        }
        catch (OperationCanceledException) { /* expected on screen teardown */ }
    }

    private async Task SubscribeAsync(string topic, HoldemEventDispatcher dispatcher, CancellationToken ct)
    {
        // Reconnect-with-backoff. IRealtimeBus.SubscribeAsync runs until either we cancel
        // or Redis drops the connection. On disconnect we resync the snapshot (to catch
        // state changes we missed during the blip) and resubscribe. Backoff caps at 5s
        // so a flapping Redis doesn't hammer reconnection attempts.
        var backoffMs = 500;
        while (!ct.IsCancellationRequested)
        {
            var disconnected = false;
            try
            {
                await foreach (var payload in _bus.SubscribeAsync(topic, ct))
                {
                    backoffMs = 500; // reset on successful delivery
                    try { dispatcher.Dispatch(payload); }
                    catch (Exception ex) { _log.LogError(ex, "dispatch failed for topic {Topic}", topic); }
                }
                disconnected = true;
            }
            catch (OperationCanceledException) { return; }
            catch (Exception ex)
            {
                _log.LogWarning(ex, "subscriber loop dropped for topic {Topic}; will reconnect", topic);
                disconnected = true;
            }
            if (!disconnected || ct.IsCancellationRequested) return;
            // Snapshot resync — we lost the live stream, so pull current state directly.
            // Best-effort; if the coordinator itself is unreachable we'll log and retry.
            try { await RefreshSnapshotAsync(ct); UiInvoke(RenderAll); }
            catch (Exception ex) { _log.LogWarning(ex, "snapshot refresh during reconnect failed"); }
            try { await Task.Delay(backoffMs, ct); } catch (OperationCanceledException) { return; }
            backoffMs = Math.Min(backoffMs * 2, 5000);
        }
    }

    private HoldemEventDispatcher PublicDispatcher() => new()
    {
        OnHandStarted = e => { ApplyHandStarted(e); EchoSystem($"-- hand #{e.HandNumber} begins (button: seat {e.DealerSeat + 1}) --"); UiInvoke(RenderAll); },
        OnBoardDealt = e => { ApplyBoardDealt(e); EchoSystem($"{e.Street}: {string.Join(' ', e.NewCards)}"); UiInvoke(RenderAll); },
        OnActionTaken = e => { ApplyActionTaken(e); EchoSystem(FormatAction(e)); UiInvoke(RenderAll); },
        OnTurnStarted = e => { ApplyTurnStarted(e); UiInvoke(RenderAll); },
        OnSeatChanged = e => { ApplySeatChanged(e); EchoSystem(FormatSeat(e)); UiInvoke(RenderAll); },
        OnHandEnded = e =>
        {
            ApplyHandEnded(e);
            foreach (var p in e.Payouts) EchoSystem($"seat {p.Seat + 1} won ${p.Amount} ({p.Reason})");
            UiInvoke(RenderAll);
        },
        OnShowdownStarted = e => { ApplyShowdown(e); EchoSystem("-- showdown --"); UiInvoke(RenderAll); },
        OnSnapshotResync = e => { ApplySnapshot(e); UiInvoke(RenderAll); },
    };

    private void EchoSystem(string text) => _chatPane?.AppendSystem(text);

    private static string FormatAction(ActionTakenDto e)
    {
        var who = $"seat {e.Seat + 1}";
        return e.Action switch
        {
            "check" => $"{who} checks",
            "call" => $"{who} calls ${e.Amount}",
            "fold" => $"{who} folds",
            "bet" => $"{who} bets ${e.Amount}",
            "raise" => $"{who} raises to ${e.Amount}",
            "all-in" => $"{who} is all-in",
            "timeout" => $"{who} times out",
            _ => $"{who} {e.Action}",
        };
    }

    private static string FormatSeat(SeatChangedDto e) => e.Kind switch
    {
        "sit-down" => $"{e.Handle} sits down at seat {e.Seat + 1} with ${e.Stack}",
        "stand-up" => $"{e.Handle ?? $"seat {e.Seat + 1}"} stands up",
        "sit-out" => $"{e.Handle ?? $"seat {e.Seat + 1}"} sits out",
        "resume" => $"{e.Handle ?? $"seat {e.Seat + 1}"} returns to play",
        "cpu-seated" => $"{e.Handle} (cpu) joins seat {e.Seat + 1} with ${e.Stack}",
        "timeout-sitout" => $"{e.Handle ?? $"seat {e.Seat + 1}"} sits out (timed out 3×)",
        _ => $"seat {e.Seat + 1}: {e.Kind}",
    };

    private HoldemEventDispatcher PrivateDispatcher() => new()
    {
        OnHoleCardsDealt = e =>
        {
            if (e.Seat != _mySeat) return;
            _myHole1 = e.Card1;
            _myHole2 = e.Card2;
            UiInvoke(RenderAll);
        },
        OnActionRejected = e => _app.Invoke(() => _actionLabel.Text = $"[!] {e.Reason}"),
    };

    // -- State application ------------------------------------------------------------

    private void ApplySnapshot(SnapshotResyncDto dto)
    {
        lock (_stateLock)
        {
            _latest = new TableSnapshot
            {
                Phase = dto.Phase,
                HandNumber = dto.HandNumber ?? 0,
                DealerSeat = dto.DealerSeat,
                ActorSeat = dto.ActorSeat,
                TurnDeadlineUtc = dto.TurnDeadlineUtc,
                CurrentBet = dto.CurrentBet,
                MinRaise = dto.MinRaise,
                Pot = dto.Pot,
                Board = dto.Board,
                Seats = dto.Seats,
            };
            // Find my seat
            for (var i = 0; i < dto.Seats.Count; i++)
            {
                if (dto.Seats[i].Kind == "user" && dto.Seats[i].Handle?.Equals(_user.Handle, StringComparison.OrdinalIgnoreCase) == true)
                {
                    _mySeat = dto.Seats[i].SeatIndex;
                    _myHole1 = dto.Seats[i].Hole1;
                    _myHole2 = dto.Seats[i].Hole2;
                    break;
                }
            }
        }
    }

    private void ApplyHandStarted(HandStartedDto e)
    {
        lock (_stateLock)
        {
            _latest = _latest with
            {
                Phase = "PreFlop",
                HandNumber = e.HandNumber,
                DealerSeat = e.DealerSeat,
                Pot = e.Seats.Sum(s => s.BetThisRound),
                Board = Array.Empty<string>(),
                Seats = e.Seats,
            };
        }
        // New hand: old hole cards no longer valid until private event arrives.
        _myHole1 = null;
        _myHole2 = null;
    }

    private void ApplyBoardDealt(BoardDealtDto e)
    {
        lock (_stateLock)
        {
            _latest = _latest with { Board = e.BoardSoFar, Phase = TitleCase(e.Street) };
        }
    }

    private void ApplyActionTaken(ActionTakenDto e)
    {
        lock (_stateLock)
        {
            var seats = _latest.Seats.ToList();
            var idx = seats.FindIndex(s => s.SeatIndex == e.Seat);
            if (idx >= 0)
            {
                seats[idx] = seats[idx] with
                {
                    Stack = e.SeatStackAfter,
                    BetThisRound = e.SeatBetAfter,
                    Status = e.Action == "fold" ? "Folded" : seats[idx].Status,
                };
                _latest = _latest with { Seats = seats, Pot = e.Pot };
            }
        }
    }

    private void ApplyTurnStarted(TurnStartedDto e)
    {
        lock (_stateLock)
        {
            _latest = _latest with { ActorSeat = e.Seat, TurnDeadlineUtc = e.DeadlineUtc, MinRaise = e.MinRaise };
        }
    }

    private void ApplySeatChanged(SeatChangedDto e)
    {
        // Could be us joining (seat assignment) — capture our seat index.
        if (e.Kind == "sit-down" && e.Handle?.Equals(_user.Handle, StringComparison.OrdinalIgnoreCase) == true)
        {
            _mySeat = e.Seat;
        }
        // Coordinator's next events will refresh seats; for now just nudge a snapshot.
        _ = RefreshSnapshotAsync(Shutdown);
    }

    private void ApplyHandEnded(HandEndedDto e)
    {
        lock (_stateLock)
        {
            var seats = _latest.Seats.ToList();
            for (var i = 0; i < seats.Count; i++)
            {
                if (e.FinalStacks.TryGetValue(seats[i].SeatIndex, out var stack))
                    seats[i] = seats[i] with { Stack = stack, BetThisRound = 0, TotalContribution = 0 };
            }
            _latest = _latest with { Seats = seats, Phase = "HandComplete", ActorSeat = null };
        }
    }

    private void ApplyShowdown(ShowdownStartedDto e)
    {
        lock (_stateLock)
        {
            var seats = _latest.Seats.ToList();
            foreach (var shown in e.ShownHands)
            {
                var idx = seats.FindIndex(s => s.SeatIndex == shown.Seat);
                if (idx >= 0) seats[idx] = seats[idx] with { Hole1 = shown.Card1, Hole2 = shown.Card2 };
            }
            _latest = _latest with { Seats = seats };
        }
    }

    // -- Rendering --------------------------------------------------------------------

    private void RenderAll()
    {
        TableSnapshot s;
        lock (_stateLock) s = _latest;

        _tableView.SetState(
            handNumber: s.HandNumber,
            phase: s.Phase,
            dealerSeat: s.DealerSeat,
            actorSeat: s.ActorSeat,
            pot: s.Pot,
            boardWire: s.Board,
            seats: s.Seats,
            mySeat: _mySeat,
            myHole1: _myHole1,
            myHole2: _myHole2);

        _actionLabel.Text = BuildActionPrompt(s);
    }

    private string BuildActionPrompt(TableSnapshot s)
    {
        if (_phase == Phase.Watching)
        {
            // Indicate whether a seat is open (encourages joining); show available
            // hotkeys for the spectator.
            var openSeat = s.Seats.Any(x => x.Kind == "empty");
            return openSeat
                ? "[S] sit down (seat open)   [Q]/[Esc] back to doors"
                : "Watching — table is full   [Q]/[Esc] back to doors";
        }
        if (!_seated) return "[Esc] back to doors";
        if (s.ActorSeat != _mySeat) return $"Waiting… (current actor: seat {(s.ActorSeat ?? -1) + 1})    [S] sit out  [Q] stand up";

        var mySeat = s.Seats.FirstOrDefault(x => x.SeatIndex == _mySeat);
        var toCall = Math.Max(0L, s.CurrentBet - (mySeat?.BetThisRound ?? 0));
        var sb = new StringBuilder("YOUR TURN: ");
        if (toCall <= 0) sb.Append("[C]heck  [B]et  ");
        else sb.Append("[C]all $").Append(toCall).Append("  [R]aise  [F]old  ");
        sb.Append("[A]ll-in   [Tab] chat   [S] sit out  [Q] stand up");
        return sb.ToString();
    }

    // -- Key handling -----------------------------------------------------------------

    private void OnKey(object? _, Key key)
    {
        if (_busy) { key.Handled = true; return; }

        // Tab focuses the chat pane. While chat has focus its own TextField swallows
        // KeyDown so the screen's game hotkeys are inert until the user presses Tab/Esc
        // to bounce back.
        if (key == Key.Tab && _chatPane is not null)
        {
            key.Handled = true;
            _chatPane.FocusInput();
            return;
        }

        if (key == Key.Esc)
        {
            key.Handled = true;
            ExitAsync(stand: _seated).FireAndLog(_services, nameof(ExitAsync));
            return;
        }

        var ch = (char)key.KeyCode;

        // AwaitingChoice phase: S/W choose sit-or-watch, everything else inert.
        if (_phase == Phase.AwaitingChoice)
        {
            if (ch is 's' or 'S')
            {
                key.Handled = true;
                BeginSitDownAsync().FireAndLog(_services, nameof(BeginSitDownAsync));
            }
            else if (ch is 'w' or 'W')
            {
                key.Handled = true;
                BeginWatchAsync().FireAndLog(_services, nameof(BeginWatchAsync));
            }
            return;
        }

        // Watching: spectator can press S to try to sit down, Esc/Q to leave.
        if (_phase == Phase.Watching)
        {
            if (ch is 's' or 'S')
            {
                key.Handled = true;
                BeginSitDownAsync().FireAndLog(_services, nameof(BeginSitDownAsync));
                return;
            }
            if (ch is 'q' or 'Q')
            {
                key.Handled = true;
                ExitAsync(stand: false).FireAndLog(_services, nameof(ExitAsync));
                return;
            }
            return;
        }

        if (_phase != Phase.Seated || !_seated) return;

        // Q stands up + exits. Distinct from Esc so the user can bail without a stand-up
        // (Esc behaviour: also stands up because seated → stand=true above).
        if (ch is 'q' or 'Q')
        {
            key.Handled = true;
            ExitAsync(stand: true).FireAndLog(_services, nameof(ExitAsync));
            return;
        }

        // Hotkeys map to action submissions. The screen never updates view locally — it
        // waits for the bus echo so spectators see the same state.
        switch (ch)
        {
            case 'c' or 'C': SubmitActionAsync("call", 0).FireAndLog(_services, "call"); key.Handled = true; break;
            case 'f' or 'F': SubmitActionAsync("fold", 0).FireAndLog(_services, "fold"); key.Handled = true; break;
            case 'b' or 'B': EnterAmountMode(AmountMode.Bet); key.Handled = true; break;
            case 'r' or 'R': EnterAmountMode(AmountMode.Raise); key.Handled = true; break;
            case 'a' or 'A': SubmitActionAsync("all-in", 0).FireAndLog(_services, "all-in"); key.Handled = true; break;
            case 's' or 'S':
                if (_handle is not null)
                    _ = _client.SitOutAsync(_handle.TableId, _user.Id, Shutdown);
                key.Handled = true;
                break;
        }
    }

    // -- Bet-amount entry -------------------------------------------------------------

    private void EnterAmountMode(AmountMode mode)
    {
        var s = _latest;
        var mySeat = s.Seats.FirstOrDefault(x => x.SeatIndex == _mySeat);
        if (mySeat is null) return;
        var stack = mySeat.Stack;
        var betSoFar = mySeat.BetThisRound;
        var bb = _handle?.BigBlind ?? 10;

        long min, max, suggested;
        if (mode == AmountMode.Bet)
        {
            // Bet only legal when no one has bet this street. Engine throws otherwise.
            if (s.CurrentBet > 0)
            {
                _actionLabel.Text = "[!] cannot bet facing a bet — use [R]aise";
                return;
            }
            min = bb;
            max = stack;
            suggested = bb;
        }
        else
        {
            // Raise to: minimum is currentBet + minRaise, max is stack + bet so far.
            min = s.CurrentBet + s.MinRaise;
            max = betSoFar + stack;
            suggested = min;
        }
        if (max < min)
        {
            _actionLabel.Text = "[!] not enough chips for a min raise — use [A]ll-in";
            return;
        }

        _amountMode = mode;
        var label = mode == AmountMode.Bet ? "Bet" : "Raise to";
        _amountHint.Text = $"{label}: min ${min}  max ${max}   [Enter] confirm  [Esc] cancel";
        _amountInput.Text = suggested.ToString();
        _amountHint.Visible = true;
        _amountInput.Visible = true;
        _actionLabel.Visible = false;
        _amountInput.SetFocus();
        SetNeedsDraw();
    }

    private void OnAmountKey(object? sender, Key key)
    {
        if (key == Key.Esc)
        {
            key.Handled = true;
            ExitAmountMode();
            return;
        }
        if (key == Key.Enter)
        {
            key.Handled = true;
            var raw = _amountInput.Text?.ToString() ?? string.Empty;
            if (!long.TryParse(raw.Trim(), out var amount))
            {
                _amountHint.Text = $"[!] '{raw}' is not a number";
                return;
            }
            var mode = _amountMode;
            ExitAmountMode();
            var action = mode == AmountMode.Bet ? "bet" : "raise";
            SubmitActionAsync(action, amount).FireAndLog(_services, action);
        }
    }

    private void ExitAmountMode()
    {
        _amountMode = AmountMode.None;
        _amountHint.Visible = false;
        _amountInput.Visible = false;
        _amountInput.Text = string.Empty;
        _actionLabel.Visible = true;
        // Bounce focus back to the screen so game hotkeys re-engage.
        SetFocus();
        SetNeedsDraw();
    }

    private async Task SubmitActionAsync(string action, long amount)
    {
        if (_handle is null) return;
        _busy = true;
        try
        {
            var result = await _client.SubmitActionAsync(_handle.TableId, _user.Id, action, amount, Shutdown);
            if (result is MultiplayerOpResult.Rejected r)
                _app.Invoke(() => _actionLabel.Text = $"[!] {r.Reason}");
            else if (result is MultiplayerOpResult.NotYourTurn)
                _app.Invoke(() => _actionLabel.Text = "[!] not your turn");
        }
        finally { _busy = false; }
    }

    private async Task ExitAsync(bool stand)
    {
        try
        {
            if (stand && _seated && _handle is not null)
            {
                await _client.StandUpAsync(_handle.TableId, _user.Id, CancellationToken.None);
            }
        }
        catch (Exception ex) { _log.LogError(ex, "stand-up on exit failed"); }
        finally { _app.Invoke(_app.RequestStop); }
    }

    private void UiInvoke(Action a) => _app.Invoke(() => { try { a(); } catch { } });

    private static string TitleCase(string s) =>
        string.IsNullOrEmpty(s) ? s : char.ToUpperInvariant(s[0]) + s[1..];

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            try { _subsCts?.Cancel(); } catch { }
            // Best-effort sit-out so other players see us drop fast; coordinator's presence
            // check will eventually cash-out abandoned seats (in a future chunk).
            if (_seated && _handle is not null)
            {
                _ = Task.Run(async () =>
                {
                    try { await _client.SitOutAsync(_handle.TableId, _user.Id, CancellationToken.None); }
                    catch { }
                });
            }
        }
        base.Dispose(disposing);
    }

    // -- Snapshot record --------------------------------------------------------------

    private sealed record TableSnapshot
    {
        public string Phase { get; init; } = "Idle";
        public int HandNumber { get; init; }
        public int DealerSeat { get; init; }
        public int? ActorSeat { get; init; }
        public DateTimeOffset? TurnDeadlineUtc { get; init; }
        public long CurrentBet { get; init; }
        public long MinRaise { get; init; }
        public long Pot { get; init; }
        public IReadOnlyList<string> Board { get; init; } = Array.Empty<string>();
        public IReadOnlyList<SeatSnapshotDto> Seats { get; init; } = Array.Empty<SeatSnapshotDto>();

        public static readonly TableSnapshot Empty = new();
    }
}
