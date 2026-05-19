using System.Collections.Concurrent;
using System.Text.Json;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem.Cpu;
using Night.Ms.SshServer.Doors.Games.Holdem.Events;
using Night.Ms.SshServer.Doors.Multiplayer;
using Night.Ms.SshServer.Realtime;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// One instance per active table. Single-writer: all state mutations run inside _writeLock,
// so the engine and the chip ledger see a consistent view. Authoritative state lives
// in-process (HoldemTableState + occupancy maps); Redis is used for pub/sub fan-out
// (IRealtimeBus) and a small seat hash that supports reattach lookup across server
// restarts. The Redis-lease + XREAD intent stream design from the plan is deferred —
// it's only useful in a multi-instance deployment, and we're single-process today.
public sealed class HoldemTableCoordinator : ITableCoordinator
{
    private const int MinSeatedToStart = HoldemRules.MinSeatedToStart;
    private const int MinTotalEntities = 2;   // CPU filler keeps table at >= this

    private readonly long _tableId;
    private readonly long _chatChannelId;
    private readonly IConnectionMultiplexer _redis;
    private readonly IRealtimeBus _bus;
    private readonly IServiceProvider _services;
    private readonly ICpuPersonaRegistry _personas;
    private readonly IGameRng _rng;
    private readonly TimeProvider _time;
    private readonly ILogger<HoldemTableCoordinator> _log;

    private readonly HoldemTableState _state;
    private readonly Dictionary<int, SeatOccupant> _occupants = new();
    private readonly Dictionary<int, ICpuStrategy> _cpuStrategies = new();
    private readonly ConcurrentDictionary<long, int> _userIdToSeat = new();
    private readonly Dictionary<int, DateTimeOffset> _lastSeenBySeat = new();
    private DateTimeOffset? _turnDeadline;
    // Set by OnHandCompleteAsync to ask the clock loop to start the next hand on its next
    // tick. Replaces a direct recursive TryStartHandAsync call, which blew the stack on
    // all-CPU tables (StartHand → CPUs play → OnHandComplete → TryStartHand → …).
    private volatile bool _pendingNextHand;

    // How long a human seat can go without a heartbeat before the coordinator cashes
    // them out and frees the seat. Five minutes per the original plan — long enough to
    // survive a flaky network or a quick reboot, short enough that humans can't grief
    // by camping a 6-max table.
    private static readonly TimeSpan AbandonedSeatTimeout = TimeSpan.FromMinutes(5);

    private readonly SemaphoreSlim _writeLock = new(1, 1);
    private CancellationTokenSource? _cts;
    private Task? _clockLoop;

    public HoldemTableCoordinator(
        long tableId,
        long chatChannelId,
        IConnectionMultiplexer redis,
        IRealtimeBus bus,
        IServiceProvider services,
        ICpuPersonaRegistry personas,
        IGameRng rng,
        TimeProvider time,
        ILogger<HoldemTableCoordinator> log)
    {
        _tableId = tableId;
        _chatChannelId = chatChannelId;
        _redis = redis;
        _bus = bus;
        _services = services;
        _personas = personas;
        _rng = rng;
        _time = time;
        _log = log;
        _state = new HoldemTableState(HoldemRules.MaxSeats, HoldemRules.DefaultSmallBlind, HoldemRules.DefaultBigBlind, rng);
    }

    public long TableId => _tableId;
    public string GameKey => "holdem";
    public bool IsRunning { get; private set; }

    public long ChatChannelId => _chatChannelId;

    public async Task StartAsync(CancellationToken ct)
    {
        if (IsRunning) return;
        IsRunning = true;
        _cts = new CancellationTokenSource();
        _clockLoop = Task.Run(() => RunClockAsync(_cts.Token));
        // Seed the table with the CPU floor so newcomers always find an action in progress.
        await EnsureCpuFloorAsync(ct);
        await TryStartHandAsync(ct);
    }

    public async Task StopAsync(CancellationToken ct)
    {
        if (!IsRunning) return;
        IsRunning = false;
        _cts?.Cancel();
        if (_clockLoop is not null)
        {
            try { await _clockLoop.WaitAsync(ct); }
            catch (OperationCanceledException) { }
        }
        await CashOutAllHumansAsync(ct);
    }

    public async ValueTask DisposeAsync()
    {
        try { await StopAsync(CancellationToken.None); }
        catch { /* best effort during shutdown */ }
        _cts?.Dispose();
        _writeLock.Dispose();
    }

    // -- ITableCoordinator: read-only surface -----------------------------------------

    public async Task<JsonDocument> GetSnapshotAsync(long viewerUserId, CancellationToken ct)
    {
        await _writeLock.WaitAsync(ct);
        try
        {
            var snapshot = BuildSnapshot(viewerUserId);
            return JsonSerializer.SerializeToDocument(snapshot);
        }
        finally { _writeLock.Release(); }
    }

    // -- ITableCoordinator: sit / stand / sit-out / resume -----------------------------

    public async Task<MultiplayerOpResult> SitDownAsync(long userId, string handle, long startingChips, int? preferredSeat, CancellationToken ct)
    {
        await _writeLock.WaitAsync(ct);
        try
        {
            if (_userIdToSeat.TryGetValue(userId, out var existing))
                return new MultiplayerOpResult.AlreadySeated(_tableId, existing);

            var seatIndex = ResolveOpenSeat(preferredSeat);
            if (seatIndex is null) return new MultiplayerOpResult.SeatFull();

            _occupants[seatIndex.Value] = new SeatOccupant.Human(userId, handle);
            _userIdToSeat[userId] = seatIndex.Value;
            _lastSeenBySeat[seatIndex.Value] = _time.GetUtcNow();
            var seat = _state.Seats[seatIndex.Value];
            seat.Stack = startingChips;
            seat.Status = _state.Phase == HoldemPhase.Idle
                ? HoldemSeatStatus.Active
                : HoldemSeatStatus.AwaitingNextHand;

            await PersistSeatAsync(seatIndex.Value, ct);
            await PublishSeatChangedAsync(seatIndex.Value, "sit-down", handle, seat.Stack, ct);
            await TryStartHandAsync(ct);
            return new MultiplayerOpResult.Ok();
        }
        finally { _writeLock.Release(); }
    }

    public async Task<MultiplayerOpResult> StandUpAsync(long userId, CancellationToken ct)
    {
        await _writeLock.WaitAsync(ct);
        try
        {
            if (!_userIdToSeat.TryRemove(userId, out var seatIndex))
                return new MultiplayerOpResult.Rejected("not seated");

            var seat = _state.Seats[seatIndex];
            var remainingChips = (int)Math.Max(0, seat.Stack);
            seat.Stack = 0;
            seat.Status = HoldemSeatStatus.Empty;
            _occupants.Remove(seatIndex);
            _lastSeenBySeat.Remove(seatIndex);

            if (remainingChips > 0)
            {
                using var scope = _services.CreateScope();
                var ledger = scope.ServiceProvider.GetRequiredService<IMultiplayerGameLedger>();
                try { await ledger.CashOutAsync(userId, GameKey, remainingChips, ct); }
                catch (Exception ex) { _log.LogError(ex, "cash-out failed for user {UserId}", userId); }
            }

            await ForgetSeatAsync(seatIndex, userId, ct);
            await PublishSeatChangedAsync(seatIndex, "stand-up", null, 0, ct);
            await EnsureCpuFloorAsync(ct);
            return new MultiplayerOpResult.Ok();
        }
        finally { _writeLock.Release(); }
    }

    public async Task<MultiplayerOpResult> SitOutAsync(long userId, CancellationToken ct)
    {
        await _writeLock.WaitAsync(ct);
        try
        {
            if (!_userIdToSeat.TryGetValue(userId, out var seatIndex))
                return new MultiplayerOpResult.Rejected("not seated");
            var seat = _state.Seats[seatIndex];
            if (seat.Status == HoldemSeatStatus.SittingOut) return new MultiplayerOpResult.Ok();

            // Mid-hand: convert Active → Folded so the engine moves past them. End-of-hand
            // promotion logic handles the rest. If they're already folded/all-in, just
            // flag SittingOut so the next StartHand skips them.
            if (seat.Status == HoldemSeatStatus.Active && _state.ActorIndex == seatIndex)
            {
                // Fold them in place if it's their turn.
                HoldemEngine.ApplyAction(_state, seatIndex, HoldemAction.Fold());
                await PublishActionTakenAsync(seatIndex, "fold", 0, ct);
            }
            seat.Status = HoldemSeatStatus.SittingOut;
            await PublishSeatChangedAsync(seatIndex, "sit-out", _occupants[seatIndex].Handle, seat.Stack, ct);
            await DriveCpuOrAdvanceAsync(ct);
            return new MultiplayerOpResult.Ok();
        }
        finally { _writeLock.Release(); }
    }

    public async Task HeartbeatAsync(long userId, CancellationToken ct)
    {
        // Cheap path: no lock needed for the update (ConcurrentDictionary handles the
        // seat-index lookup; _lastSeenBySeat is only mutated here and inside the write
        // lock, so a single write outside the lock is benign — worst case we miss an
        // abandoned-seat sweep cycle).
        if (_userIdToSeat.TryGetValue(userId, out var seatIndex))
        {
            _lastSeenBySeat[seatIndex] = _time.GetUtcNow();
        }
        await Task.CompletedTask;
    }

    public async Task<MultiplayerOpResult> ResumeAsync(long userId, CancellationToken ct)
    {
        await _writeLock.WaitAsync(ct);
        try
        {
            if (!_userIdToSeat.TryGetValue(userId, out var seatIndex))
                return new MultiplayerOpResult.Rejected("not seated");
            var seat = _state.Seats[seatIndex];
            if (seat.Status != HoldemSeatStatus.SittingOut)
                return new MultiplayerOpResult.Rejected("not sitting out");
            seat.Status = _state.Phase == HoldemPhase.Idle
                ? HoldemSeatStatus.Active
                : HoldemSeatStatus.AwaitingNextHand;
            await PublishSeatChangedAsync(seatIndex, "resume", _occupants[seatIndex].Handle, seat.Stack, ct);
            await TryStartHandAsync(ct);
            return new MultiplayerOpResult.Ok();
        }
        finally { _writeLock.Release(); }
    }

    // -- ITableCoordinator: intent submission -----------------------------------------

    public async Task<MultiplayerOpResult> SubmitIntentAsync(long userId, JsonDocument intent, CancellationToken ct)
    {
        await _writeLock.WaitAsync(ct);
        try
        {
            if (!_userIdToSeat.TryGetValue(userId, out var seatIndex))
                return new MultiplayerOpResult.Rejected("not seated");
            if (_state.ActorIndex != seatIndex)
                return new MultiplayerOpResult.NotYourTurn();
            // Any user-driven intent is itself a liveness signal.
            _lastSeenBySeat[seatIndex] = _time.GetUtcNow();

            HoldemAction action;
            try { action = ParseIntent(intent); }
            catch (Exception ex)
            {
                await PublishPrivateAsync(userId, PokerEventKinds.ActionRejected,
                    new ActionRejectedDto(_state.HandNumber, ex.Message), ct);
                return new MultiplayerOpResult.Rejected(ex.Message);
            }

            try { HoldemEngine.ApplyAction(_state, seatIndex, action); }
            catch (InvalidOperationException ex)
            {
                await PublishPrivateAsync(userId, PokerEventKinds.ActionRejected,
                    new ActionRejectedDto(_state.HandNumber, ex.Message), ct);
                return new MultiplayerOpResult.Rejected(ex.Message);
            }

            await PublishActionTakenAsync(seatIndex, WireActionName(action.Kind), action.Amount, ct);
            await AfterActionAdvanceAsync(ct);
            return new MultiplayerOpResult.Ok();
        }
        finally { _writeLock.Release(); }
    }

    // -- Hand lifecycle ---------------------------------------------------------------

    private async Task TryStartHandAsync(CancellationToken ct)
    {
        if (_state.Phase != HoldemPhase.Idle && _state.Phase != HoldemPhase.HandComplete) return;

        // Mirror HoldemEngine.StartHand's promotion: Folded/AllIn/AwaitingNextHand seats
        // with chips will be reset to Active on the next deal. Only Empty/SittingOut are
        // excluded. Without this, a hand that ended on a fold leaves the loser status=Folded
        // and the dealable count stays below the minimum until something else nudges state.
        var dealable = _state.Seats.Count(s =>
            s.Stack > 0
            && s.Status is not (HoldemSeatStatus.Empty or HoldemSeatStatus.SittingOut));
        if (dealable < MinSeatedToStart) return;

        var nextButton = ChooseNextButton();
        _state.RebindDeck(new Deck(_rng));
        HoldemEngine.StartHand(_state, nextButton);

        await PublishHandStartedAsync(ct);
        await PublishHoleCardsToHumansAsync(ct);
        await PublishTurnStartedAsync(ct);
        await DriveCpuOrAdvanceAsync(ct);
    }

    private int ChooseNextButton()
    {
        // Advance from the current button to the next seat with chips that's not sitting-out.
        for (var step = 1; step <= _state.Seats.Count; step++)
        {
            var candidate = (_state.DealerButton + step) % _state.Seats.Count;
            var s = _state.Seats[candidate];
            if (s.Stack > 0 && s.Status != HoldemSeatStatus.SittingOut && s.Status != HoldemSeatStatus.Empty)
                return candidate;
        }
        return _state.DealerButton; // fallback
    }

    // -- Engine ↔ bus glue ------------------------------------------------------------

    private async Task AfterActionAdvanceAsync(CancellationToken ct)
    {
        if (_state.Phase == HoldemPhase.HandComplete)
        {
            await OnHandCompleteAsync(ct);
            return;
        }
        // Engine internally advances board + actor; emit any new street + new turn-started.
        // The engine may have dealt new board cards as part of street advance — we don't
        // know exactly when, so just publish the current board state if it grew.
        await PublishBoardIfAdvancedAsync(ct);
        await PublishTurnStartedAsync(ct);
        await DriveCpuOrAdvanceAsync(ct);
    }

    private int _lastBoardCount;
    private async Task PublishBoardIfAdvancedAsync(CancellationToken ct)
    {
        if (_state.Board.Count <= _lastBoardCount) return;
        var newCount = _state.Board.Count;
        var newCards = _state.Board.Skip(_lastBoardCount).Select(CardWire.Encode).ToList();
        var street = newCount switch { 3 => "flop", 4 => "turn", 5 => "river", _ => "unknown" };
        _lastBoardCount = newCount;
        await PublishAsync(PokerEventKinds.BoardDealt, new BoardDealtDto(
            _state.HandNumber, street, newCards, _state.Board.Select(CardWire.Encode).ToList()), ct);
    }

    private async Task DriveCpuOrAdvanceAsync(CancellationToken ct)
    {
        // After any state advance, if the actor is a CPU, let them act. Loop until either
        // a human is up next or the hand ends.
        while (_state.Phase != HoldemPhase.HandComplete && _state.ActorIndex is int actor)
        {
            if (!IsCpuSeat(actor)) return;
            var strategy = _cpuStrategies[actor];
            var action = strategy.Decide(_state, actor);
            await ApplyCpuThinkDelayAsync(action, ct);
            try { HoldemEngine.ApplyAction(_state, actor, action); }
            catch (InvalidOperationException ex)
            {
                _log.LogWarning(ex, "CPU at seat {Seat} produced illegal action; defaulting", actor);
                HoldemEngine.ApplyAction(_state, actor, HoldemAction.Default());
            }
            await PublishActionTakenAsync(actor, WireActionName(action.Kind), action.Amount, ct);
            await PublishBoardIfAdvancedAsync(ct);
            await PublishTurnStartedAsync(ct);
        }
        if (_state.Phase == HoldemPhase.HandComplete) await OnHandCompleteAsync(ct);
    }

    // Lock-held sleep between deciding and applying a CPU action. We deliberately don't
    // release the write lock during the delay: the clock loop's timeout sweep would
    // otherwise fire ApplyTimeout on the actor mid-think and the resume-after-delay would
    // call ApplyAction on a now-Folded seat. Snapshots can stall briefly but only for one
    // CPU's think-time; humans on this table are blocked anyway because it isn't their turn.
    private async Task ApplyCpuThinkDelayAsync(HoldemAction action, CancellationToken ct)
    {
        var max = HoldemRules.CpuThinkMax;
        if (max <= TimeSpan.Zero) return;
        var min = HoldemRules.CpuThinkMin;
        if (min < TimeSpan.Zero) min = TimeSpan.Zero;
        if (min > max) min = max;

        // Per-action scaling: forced/cheap moves are quick, real decisions take longer.
        var (lowFrac, highFrac) = action.Kind switch
        {
            HoldemActionKind.Fold => (0.20, 0.55),
            HoldemActionKind.Check => (0.30, 0.65),
            HoldemActionKind.Call => (0.45, 0.85),
            HoldemActionKind.Bet or HoldemActionKind.Raise => (0.70, 1.00),
            HoldemActionKind.AllIn => (0.85, 1.00),
            _ => (0.50, 0.85),
        };

        var range = (max - min).TotalMilliseconds;
        var low = min.TotalMilliseconds + lowFrac * range;
        var high = min.TotalMilliseconds + highFrac * range;
        var ms = (int)Math.Round(low + _rng.NextDouble() * (high - low));
        if (ms <= 0) return;
        try { await Task.Delay(ms, ct); }
        catch (OperationCanceledException) { /* shutdown */ }
    }

    private async Task OnHandCompleteAsync(CancellationToken ct)
    {
        // Build per-player movements for the ledger.
        var movements = new List<PlayerMovement>();
        var finalStacks = new Dictionary<int, long>();
        HoldemEngine.Settle(_state);

        for (var i = 0; i < _state.Seats.Count; i++)
        {
            var seat = _state.Seats[i];
            if (!_occupants.TryGetValue(i, out var occupant)) continue;
            var wagered = (int)seat.TotalContribution;
            var payout = (int)_state.Payouts.Where(p => p.SeatIndex == i).Sum(p => p.Amount);
            if (wagered == 0 && payout == 0) continue;
            finalStacks[i] = seat.Stack;
            movements.Add(new PlayerMovement(
                UserId: occupant is SeatOccupant.Human h ? h.UserId : null,
                Handle: occupant.Handle,
                WageredThisHand: wagered,
                Payout: payout,
                ChipStackAfter: seat.Stack));
        }

        // Persist atomic settlement (humans only; CPUs skipped).
        if (movements.Any(m => m.UserId is not null))
        {
            using var scope = _services.CreateScope();
            var ledger = scope.ServiceProvider.GetRequiredService<IMultiplayerGameLedger>();
            var details = JsonSerializer.SerializeToDocument(new
            {
                board = _state.Board.Select(CardWire.Encode).ToList(),
                payouts = _state.Payouts,
            });
            try { await ledger.SettleHandAsync(new SettleHand(GameKey, _tableId, _state.HandNumber, movements, details), ct); }
            catch (Exception ex) { _log.LogError(ex, "settle failed table={Table} hand={Hand}", _tableId, _state.HandNumber); }
        }

        // Showdown reveal: only seats still alive at showdown reveal their hole cards.
        var shown = new List<ShownHandDto>();
        if (_state.Board.Count == 5)
        {
            for (var i = 0; i < _state.Seats.Count; i++)
            {
                var seat = _state.Seats[i];
                if (seat.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AllIn
                    && seat.Hole1 is not null && seat.Hole2 is not null)
                {
                    shown.Add(new ShownHandDto(i, CardWire.Encode(seat.Hole1), CardWire.Encode(seat.Hole2), false));
                }
            }
            await PublishAsync(PokerEventKinds.ShowdownStarted, new ShowdownStartedDto(_state.HandNumber, shown), ct);
        }

        await PublishAsync(PokerEventKinds.HandEnded, new HandEndedDto(
            _state.HandNumber,
            _state.Payouts.Select(p => new HandPayoutDto(p.SeatIndex, p.Amount, p.Reason)).ToList(),
            finalStacks), ct);

        // Persist updated stacks back to Redis seat hash.
        foreach (var idx in finalStacks.Keys) await PersistSeatAsync(idx, ct);

        // Bust-out: empty seats whose stack is 0 (CPUs only — humans keep their seat in
        // SittingOut so they can reload by standing up + sitting down again).
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            if (_state.Seats[i].Stack > 0) continue;
            if (!_occupants.TryGetValue(i, out var occupant)) continue;
            if (occupant is SeatOccupant.Cpu)
            {
                _occupants.Remove(i);
                _cpuStrategies.Remove(i);
                _state.Seats[i].Status = HoldemSeatStatus.Empty;
                await ForgetSeatAsync(i, null, ct);
                await PublishSeatChangedAsync(i, "stand-up", occupant.Handle, 0, ct);
            }
        }

        _lastBoardCount = 0;
        _turnDeadline = null;
        _state.Phase = HoldemPhase.Idle;

        await EnsureCpuFloorAsync(ct);
        // Don't recurse into TryStartHandAsync here — DriveCpuOrAdvanceAsync is on the stack
        // above us via the CPU loop, and starting another hand would re-enter it and
        // eventually blow the stack on all-CPU tables. The clock loop picks this up on the
        // next 1Hz tick, which also rate-limits all-CPU runaway at ~1 hand/second.
        _pendingNextHand = true;
    }

    // -- Publish helpers --------------------------------------------------------------

    private Task PublishAsync<T>(string kind, T payload, CancellationToken ct)
    {
        var envelope = new PokerEventEnvelope(kind, JsonSerializer.SerializeToElement(payload));
        var bytes = JsonSerializer.SerializeToUtf8Bytes(envelope);
        return _bus.PublishAsync(MultiplayerTopics.Events(GameKey, _tableId), bytes, ct);
    }

    private Task PublishPrivateAsync<T>(long userId, string kind, T payload, CancellationToken ct)
    {
        var envelope = new PokerEventEnvelope(kind, JsonSerializer.SerializeToElement(payload));
        var bytes = JsonSerializer.SerializeToUtf8Bytes(envelope);
        return _bus.PublishAsync(MultiplayerTopics.Private(GameKey, _tableId, userId), bytes, ct);
    }

    private async Task PublishHandStartedAsync(CancellationToken ct)
    {
        // Determine SB/BB seats by scanning seats with BetThisRound > 0 (only blinds posted
        // so far).
        var sb = -1; var bb = -1;
        long sbBet = long.MaxValue;
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            if (_state.Seats[i].BetThisRound > 0 && _state.Seats[i].BetThisRound < sbBet)
            {
                sb = i; sbBet = _state.Seats[i].BetThisRound;
            }
        }
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            if (_state.Seats[i].BetThisRound > 0 && i != sb) { bb = i; break; }
        }

        var seats = BuildPublicSeatSnapshots();
        await PublishAsync(PokerEventKinds.HandStarted, new HandStartedDto(
            _state.HandNumber, _state.DealerButton, sb, bb, _state.SmallBlind, _state.BigBlind, seats), ct);
    }

    private async Task PublishHoleCardsToHumansAsync(CancellationToken ct)
    {
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            var seat = _state.Seats[i];
            if (seat.Hole1 is null || seat.Hole2 is null) continue;
            if (!_occupants.TryGetValue(i, out var occupant)) continue;
            if (occupant is not SeatOccupant.Human h) continue;
            var dto = new HoleCardsDealtDto(_state.HandNumber, i, CardWire.Encode(seat.Hole1), CardWire.Encode(seat.Hole2));
            await PublishPrivateAsync(h.UserId, PokerEventKinds.HoleCardsDealt, dto, ct);
        }
    }

    private async Task PublishTurnStartedAsync(CancellationToken ct)
    {
        if (_state.ActorIndex is not int actor) return;
        var legal = HoldemEngine.LegalActions(_state, actor);
        var seat = _state.Seats[actor];
        var toCall = Math.Max(0, _state.CurrentBet - seat.BetThisRound);
        _turnDeadline = _time.GetUtcNow().AddSeconds(HoldemRules.TurnSeconds);
        await PublishAsync(PokerEventKinds.TurnStarted, new TurnStartedDto(
            _state.HandNumber, actor, _turnDeadline.Value, _state.MinRaise, toCall,
            legal.Select(a => WireActionName(a.Kind)).ToList()), ct);
    }

    private async Task PublishActionTakenAsync(int seatIndex, string action, long amount, CancellationToken ct)
    {
        var seat = _state.Seats[seatIndex];
        var pot = _state.Seats.Sum(s => s.TotalContribution);
        await PublishAsync(PokerEventKinds.ActionTaken, new ActionTakenDto(
            _state.HandNumber, seatIndex, action, amount, pot, seat.Stack, seat.BetThisRound), ct);
    }

    private Task PublishSeatChangedAsync(int seatIndex, string kind, string? handle, long stack, CancellationToken ct) =>
        PublishAsync(PokerEventKinds.SeatChanged, new SeatChangedDto(seatIndex, kind, handle, stack), ct);

    // -- Snapshot builder -------------------------------------------------------------

    private SnapshotResyncDto BuildSnapshot(long viewerUserId)
    {
        var board = _state.Board.Select(CardWire.Encode).ToList();
        var pot = _state.Seats.Sum(s => s.TotalContribution);
        var seats = new List<SeatSnapshotDto>(_state.Seats.Count);
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            var seat = _state.Seats[i];
            _occupants.TryGetValue(i, out var occupant);
            var kind = occupant switch
            {
                SeatOccupant.Human => "user",
                SeatOccupant.Cpu => "cpu",
                _ => "empty",
            };
            // Reveal hole cards only to the seat's owner (or at showdown if we ever wire
            // that path through the snapshot).
            var revealOwn = occupant is SeatOccupant.Human h && h.UserId == viewerUserId;
            seats.Add(new SeatSnapshotDto(
                SeatIndex: i,
                Handle: occupant?.Handle,
                Kind: kind,
                Stack: seat.Stack,
                BetThisRound: seat.BetThisRound,
                TotalContribution: seat.TotalContribution,
                Status: seat.Status.ToString(),
                Hole1: revealOwn && seat.Hole1 is not null ? CardWire.Encode(seat.Hole1) : null,
                Hole2: revealOwn && seat.Hole2 is not null ? CardWire.Encode(seat.Hole2) : null));
        }
        return new SnapshotResyncDto(
            ViewerUserId: viewerUserId,
            HandNumber: _state.Phase == HoldemPhase.Idle ? null : _state.HandNumber,
            Phase: _state.Phase.ToString(),
            DealerSeat: _state.DealerButton,
            ActorSeat: _state.ActorIndex,
            TurnDeadlineUtc: _turnDeadline,
            CurrentBet: _state.CurrentBet,
            MinRaise: _state.MinRaise,
            SmallBlind: _state.SmallBlind,
            BigBlind: _state.BigBlind,
            Pot: pot,
            Board: board,
            Seats: seats);
    }

    private List<SeatSnapshotDto> BuildPublicSeatSnapshots()
    {
        var seats = new List<SeatSnapshotDto>(_state.Seats.Count);
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            var seat = _state.Seats[i];
            _occupants.TryGetValue(i, out var occupant);
            var kind = occupant switch
            {
                SeatOccupant.Human => "user",
                SeatOccupant.Cpu => "cpu",
                _ => "empty",
            };
            seats.Add(new SeatSnapshotDto(i, occupant?.Handle, kind,
                seat.Stack, seat.BetThisRound, seat.TotalContribution, seat.Status.ToString(),
                Hole1: null, Hole2: null));
        }
        return seats;
    }

    // -- CPU filler -------------------------------------------------------------------

    private async Task EnsureCpuFloorAsync(CancellationToken ct)
    {
        var occupied = _occupants.Count;
        if (occupied >= MinTotalEntities) return;

        var alreadySeated = _occupants.Values
            .OfType<SeatOccupant.Cpu>()
            .Select(c => c.PersonaId)
            .ToHashSet();
        var candidates = _personas.ForGame(GameKey)
            .Where(p => !alreadySeated.Contains(p.Id))
            .ToList();
        if (candidates.Count == 0) return;

        while (_occupants.Count < MinTotalEntities)
        {
            var seat = ResolveOpenSeat(null);
            if (seat is null) break;
            var persona = candidates[_rng.Next(candidates.Count)];
            candidates.Remove(persona);
            var strategy = StrategyFor(persona);
            _occupants[seat.Value] = new SeatOccupant.Cpu(persona.Id, persona.Handle);
            _cpuStrategies[seat.Value] = strategy;
            var s = _state.Seats[seat.Value];
            s.Stack = HoldemRules.DefaultMinBuyInChips * 5;     // 500 chip starting CPU stack
            s.Status = _state.Phase == HoldemPhase.Idle
                ? HoldemSeatStatus.Active
                : HoldemSeatStatus.AwaitingNextHand;
            await PersistSeatAsync(seat.Value, ct);
            await PublishSeatChangedAsync(seat.Value, "cpu-seated", persona.Handle, s.Stack, ct);
        }
    }

    private static ICpuStrategy StrategyFor(CpuPersona persona)
    {
        var personality = CpuPersonalities.ByName(persona.PolicyKey) ?? CpuPersonalities.Balanced;
        return new EquityCpuStrategy(personality);
    }

    private bool IsCpuSeat(int seatIndex) =>
        _occupants.TryGetValue(seatIndex, out var occupant) && occupant is SeatOccupant.Cpu;

    private int? ResolveOpenSeat(int? preferred)
    {
        if (preferred is int p && p >= 0 && p < _state.Seats.Count && !_occupants.ContainsKey(p))
            return p;
        for (var i = 0; i < _state.Seats.Count; i++)
        {
            if (!_occupants.ContainsKey(i)) return i;
        }
        return null;
    }

    // -- Redis seat hash --------------------------------------------------------------

    private async Task PersistSeatAsync(int seatIndex, CancellationToken ct)
    {
        var db = _redis.GetDatabase();
        var key = MultiplayerTopics.SeatsKey(GameKey, _tableId);
        if (!_occupants.TryGetValue(seatIndex, out var occupant))
        {
            await db.HashDeleteAsync(key, seatIndex.ToString());
            return;
        }
        var seat = _state.Seats[seatIndex];
        var payload = JsonSerializer.Serialize(new
        {
            kind = occupant is SeatOccupant.Human ? "user" : "cpu",
            userId = (occupant as SeatOccupant.Human)?.UserId,
            personaId = (occupant as SeatOccupant.Cpu)?.PersonaId,
            handle = occupant.Handle,
            chips = seat.Stack,
            status = seat.Status.ToString(),
            lastSeen = _time.GetUtcNow().ToUnixTimeSeconds(),
        });
        await db.HashSetAsync(key, seatIndex.ToString(), payload);
        if (occupant is SeatOccupant.Human h)
        {
            await db.StringSetAsync(MultiplayerTopics.UserSeatKey(GameKey, h.UserId), _tableId.ToString());
        }
    }

    private async Task ForgetSeatAsync(int seatIndex, long? userId, CancellationToken ct)
    {
        var db = _redis.GetDatabase();
        await db.HashDeleteAsync(MultiplayerTopics.SeatsKey(GameKey, _tableId), seatIndex.ToString());
        if (userId is long uid) await db.KeyDeleteAsync(MultiplayerTopics.UserSeatKey(GameKey, uid));
    }

    private async Task CashOutAllHumansAsync(CancellationToken ct)
    {
        var snapshot = _occupants.ToList();
        foreach (var (idx, occupant) in snapshot)
        {
            if (occupant is not SeatOccupant.Human h) continue;
            try { await StandUpAsync(h.UserId, ct); }
            catch (Exception ex) { _log.LogError(ex, "stand-up failed during shutdown for {UserId}", h.UserId); }
        }
    }

    // -- Clock loop -------------------------------------------------------------------

    private async Task RunClockAsync(CancellationToken ct)
    {
        // Two responsibilities, both polled on a 1Hz timer:
        //   1. Turn timeout: if the current actor's deadline has elapsed, apply Default
        //      (engine resolves to Check/Fold).
        //   2. Abandoned seats: every ~15s, scan human seats whose lastSeenAt is older
        //      than AbandonedSeatTimeout. Cash them out and free the seat. The CPU
        //      floor is restored by the existing fill logic.
        try
        {
            using var timer = new PeriodicTimer(TimeSpan.FromSeconds(1));
            var lastAbandonedScan = _time.GetUtcNow();
            while (await timer.WaitForNextTickAsync(ct))
            {
                var now = _time.GetUtcNow();

                if (_turnDeadline is DateTimeOffset deadline && now >= deadline)
                {
                    await _writeLock.WaitAsync(ct);
                    try
                    {
                        if (_state.ActorIndex is int actor && _turnDeadline is DateTimeOffset d && _time.GetUtcNow() >= d)
                        {
                            HoldemEngine.ApplyTimeout(_state, actor);
                            await PublishActionTakenAsync(actor, "timeout", 0, ct);
                            _turnDeadline = null;
                            await AfterActionAdvanceAsync(ct);
                        }
                    }
                    finally { _writeLock.Release(); }
                }

                if ((now - lastAbandonedScan).TotalSeconds >= 15)
                {
                    lastAbandonedScan = now;
                    await SweepAbandonedSeatsAsync(now, ct);
                }

                if (_pendingNextHand)
                {
                    _pendingNextHand = false;
                    await _writeLock.WaitAsync(ct);
                    try { await TryStartHandAsync(ct); }
                    finally { _writeLock.Release(); }
                }
            }
        }
        catch (OperationCanceledException) { /* expected on shutdown */ }
        catch (Exception ex) { _log.LogError(ex, "clock loop crashed"); }
    }

    private async Task SweepAbandonedSeatsAsync(DateTimeOffset now, CancellationToken ct)
    {
        // Identify abandoned humans outside the write lock to keep the scan cheap.
        List<long>? abandoned = null;
        foreach (var (seatIndex, occupant) in _occupants.ToList())
        {
            if (occupant is not SeatOccupant.Human h) continue;
            if (!_lastSeenBySeat.TryGetValue(seatIndex, out var lastSeen))
            {
                // No heartbeat ever recorded — treat sit-down time as last-seen by
                // setting it now; sweep won't act on this seat until next interval.
                _lastSeenBySeat[seatIndex] = now;
                continue;
            }
            if (now - lastSeen > AbandonedSeatTimeout)
            {
                abandoned ??= new List<long>();
                abandoned.Add(h.UserId);
            }
        }
        if (abandoned is null) return;

        foreach (var uid in abandoned)
        {
            try
            {
                _log.LogInformation("table {Table}: cashing out abandoned seat for user {UserId}", _tableId, uid);
                await StandUpAsync(uid, ct);
            }
            catch (Exception ex)
            {
                _log.LogError(ex, "abandoned-seat cleanup failed for user {UserId}", uid);
            }
        }
    }

    // -- Intent parsing ---------------------------------------------------------------

    private static HoldemAction ParseIntent(JsonDocument intent)
    {
        var root = intent.RootElement;
        var action = root.GetProperty("action").GetString() ?? throw new ArgumentException("missing action");
        long amount = 0;
        if (root.TryGetProperty("amount", out var amt) && amt.ValueKind == JsonValueKind.Number)
            amount = amt.GetInt64();
        return action.ToLowerInvariant() switch
        {
            "check" => HoldemAction.Check(),
            "call" => HoldemAction.Call(),
            "fold" => HoldemAction.Fold(),
            "bet" => HoldemAction.Bet(amount),
            "raise" => HoldemAction.Raise(amount),
            "all-in" or "allin" => HoldemAction.AllIn(),
            _ => throw new ArgumentException($"unknown action '{action}'"),
        };
    }

    private static string WireActionName(HoldemActionKind kind) => kind switch
    {
        HoldemActionKind.Check => "check",
        HoldemActionKind.Call => "call",
        HoldemActionKind.Fold => "fold",
        HoldemActionKind.Bet => "bet",
        HoldemActionKind.Raise => "raise",
        HoldemActionKind.AllIn => "all-in",
        HoldemActionKind.Default => "timeout",
        _ => "unknown",
    };
}
