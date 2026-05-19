using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem.Evaluator;

namespace Night.Ms.SshServer.Doors.Games.Holdem;

// Pure rules for No-Limit Texas Hold'em. Static methods only; state mutation happens on the
// HoldemTableState the caller hands in. Mirrors the BlackjackEngine pattern.
//
// HasOption tracks "must this seat still act this betting round." A round closes when no
// Active seat has HasOption == true. Folded and AllIn seats never gate the round.
public static class HoldemEngine
{
    // -- Hand lifecycle -----------------------------------------------------------------

    public static void StartHand(HoldemTableState state, int dealerButtonIndex)
    {
        // Reset table.
        state.HandNumber++;
        state.Board.Clear();
        state.CurrentBet = 0;
        state.MinRaise = state.BigBlind;
        state.Pots = null;
        state.Payouts.Clear();

        // Reset every seat — must happen before the live-seat headcount so seats that
        // ended last hand Folded/AllIn but still have chips get promoted back to Active.
        for (var i = 0; i < state.Seats.Count; i++)
        {
            var s = state.Seats[i];
            s.BetThisRound = 0;
            s.TotalContribution = 0;
            s.Hole1 = null;
            s.Hole2 = null;
            s.HasOption = false;
            if (s.Status == HoldemSeatStatus.AwaitingNextHand) s.Status = HoldemSeatStatus.Active;
            if (s.Status == HoldemSeatStatus.Folded || s.Status == HoldemSeatStatus.AllIn)
                s.Status = s.Stack > 0 ? HoldemSeatStatus.Active : HoldemSeatStatus.SittingOut;
        }

        var liveSeats = state.Seats
            .Select((s, i) => (s, i))
            .Where(t => CanDealInto(t.s))
            .ToList();

        if (liveSeats.Count < HoldemRules.MinSeatedToStart)
            throw new InvalidOperationException(
                $"need at least {HoldemRules.MinSeatedToStart} live seats, found {liveSeats.Count}");

        state.DealerButton = dealerButtonIndex;

        // Post blinds. Heads-up: dealer button posts SB (per standard rule). Otherwise SB
        // is the next live seat clockwise of the button.
        var activeIndices = liveSeats.Where(t => t.s.Status == HoldemSeatStatus.Active).Select(t => t.i).ToList();
        int sbIndex, bbIndex;
        if (activeIndices.Count == 2)
        {
            sbIndex = dealerButtonIndex;
            bbIndex = NextLivePlayer(state, sbIndex);
        }
        else
        {
            sbIndex = NextLivePlayer(state, dealerButtonIndex);
            bbIndex = NextLivePlayer(state, sbIndex);
        }

        PostBlind(state, sbIndex, state.SmallBlind);
        PostBlind(state, bbIndex, state.BigBlind);

        // Deal 2 hole cards each, starting left of button, two passes.
        var firstToDeal = NextLivePlayer(state, dealerButtonIndex);
        DealHoleCards(state, firstToDeal);

        state.CurrentBet = state.BigBlind;
        state.MinRaise = state.BigBlind;

        // Everyone in the hand has HasOption=true at the start of the round. The BB will
        // get their "option" naturally because BetThisRound==CurrentBet on them already;
        // they will be the last to be polled by AdvanceActor.
        foreach (var seat in state.Seats)
        {
            if (seat.Status == HoldemSeatStatus.Active) seat.HasOption = true;
        }

        state.Phase = HoldemPhase.PreFlop;
        state.ActorIndex = NextLivePlayer(state, bbIndex);
    }

    // -- Action queries -----------------------------------------------------------------

    public static IReadOnlyList<HoldemAction> LegalActions(HoldemTableState state, int seatIndex)
    {
        var seat = state.Seats[seatIndex];
        if (state.ActorIndex != seatIndex) return Array.Empty<HoldemAction>();
        if (seat.Status != HoldemSeatStatus.Active) return Array.Empty<HoldemAction>();
        if (state.Phase is HoldemPhase.Idle or HoldemPhase.Showdown or HoldemPhase.HandComplete)
            return Array.Empty<HoldemAction>();

        var legal = new List<HoldemAction>(6);
        var toCall = state.CurrentBet - seat.BetThisRound;

        if (toCall <= 0)
        {
            // No bet to face: check or bet/all-in.
            legal.Add(HoldemAction.Check());
            if (seat.Stack >= state.BigBlind)
            {
                // Open bet must be at least 1 BB. UI clamps the exact amount in BetRange.
                legal.Add(HoldemAction.Bet(state.BigBlind));
            }
            if (seat.Stack > 0)
                legal.Add(HoldemAction.AllIn());
        }
        else
        {
            legal.Add(HoldemAction.Fold());
            if (seat.Stack >= toCall) legal.Add(HoldemAction.Call());
            // Min legal raise target = CurrentBet + MinRaise. Caller must have enough chips
            // to *reach* that target; if not, AllIn is the only way to raise.
            var minRaiseTo = state.CurrentBet + state.MinRaise;
            var chipsForMinRaise = minRaiseTo - seat.BetThisRound;
            if (seat.Stack > toCall && seat.Stack >= chipsForMinRaise)
                legal.Add(HoldemAction.Raise(minRaiseTo));
            if (seat.Stack > 0)
                legal.Add(HoldemAction.AllIn());
        }
        return legal;
    }

    // -- Action application -------------------------------------------------------------

    public static void ApplyAction(HoldemTableState state, int seatIndex, HoldemAction action)
    {
        if (state.ActorIndex != seatIndex)
            throw new InvalidOperationException($"seat {seatIndex} is not the actor (actor={state.ActorIndex})");

        if (action.Kind == HoldemActionKind.Default)
        {
            ApplyTimeout(state, seatIndex);
            return;
        }

        var seat = state.Seats[seatIndex];
        if (seat.Status != HoldemSeatStatus.Active)
            throw new InvalidOperationException($"seat {seatIndex} not active (status={seat.Status})");

        // Any user-driven action resets the miss counter.
        state.ConsecutiveMisses[seatIndex] = 0;

        switch (action.Kind)
        {
            case HoldemActionKind.Fold:
                seat.Status = HoldemSeatStatus.Folded;
                seat.HasOption = false;
                AfterActionCheckForLastStanding(state);
                // Short-circuit can mark the hand complete; in that case ActorIndex was
                // nulled and there's no round to advance.
                if (state.Phase == HoldemPhase.HandComplete) return;
                AdvanceActorOrEndRound(state);
                return;
            case HoldemActionKind.Check:
                if (state.CurrentBet != seat.BetThisRound)
                    throw new InvalidOperationException("cannot check facing a bet");
                seat.HasOption = false;
                AdvanceActorOrEndRound(state);
                return;
            case HoldemActionKind.Call:
                {
                    var toCall = state.CurrentBet - seat.BetThisRound;
                    if (toCall <= 0)
                        throw new InvalidOperationException("nothing to call; use Check");
                    var spent = Math.Min(toCall, seat.Stack);
                    Commit(seat, spent);
                    if (seat.Stack == 0) seat.Status = HoldemSeatStatus.AllIn;
                    seat.HasOption = false;
                    AdvanceActorOrEndRound(state);
                    return;
                }
            case HoldemActionKind.Bet:
                {
                    if (state.CurrentBet != 0)
                        throw new InvalidOperationException("cannot bet facing a bet; use Raise");
                    var target = action.Amount;
                    if (target < state.BigBlind)
                        throw new InvalidOperationException($"bet must be ≥ {state.BigBlind}");
                    if (target > seat.Stack)
                        throw new InvalidOperationException("bet exceeds stack");
                    Commit(seat, target);
                    if (seat.Stack == 0) seat.Status = HoldemSeatStatus.AllIn;
                    state.CurrentBet = seat.BetThisRound;
                    state.MinRaise = target;
                    seat.HasOption = false;
                    ReopenActionAfterFullRaise(state, seatIndex);
                    AdvanceActorOrEndRound(state);
                    return;
                }
            case HoldemActionKind.Raise:
                {
                    if (state.CurrentBet == 0)
                        throw new InvalidOperationException("nothing to raise; use Bet");
                    var target = action.Amount;
                    var increment = target - state.CurrentBet;
                    var spent = target - seat.BetThisRound;
                    if (increment < state.MinRaise && spent < seat.Stack)
                        throw new InvalidOperationException($"raise increment {increment} < min raise {state.MinRaise}");
                    if (spent > seat.Stack)
                        throw new InvalidOperationException("raise exceeds stack");
                    Commit(seat, spent);
                    if (seat.Stack == 0) seat.Status = HoldemSeatStatus.AllIn;
                    var isFullRaise = increment >= state.MinRaise;
                    state.CurrentBet = seat.BetThisRound;
                    if (isFullRaise) state.MinRaise = increment;
                    seat.HasOption = false;
                    if (isFullRaise) ReopenActionAfterFullRaise(state, seatIndex);
                    AdvanceActorOrEndRound(state);
                    return;
                }
            case HoldemActionKind.AllIn:
                {
                    var spent = seat.Stack;
                    if (spent <= 0)
                        throw new InvalidOperationException("nothing to push");
                    Commit(seat, spent);
                    seat.Status = HoldemSeatStatus.AllIn;
                    var newBet = seat.BetThisRound;
                    if (newBet > state.CurrentBet)
                    {
                        var increment = newBet - state.CurrentBet;
                        var isFullRaise = increment >= state.MinRaise;
                        state.CurrentBet = newBet;
                        if (isFullRaise)
                        {
                            state.MinRaise = increment;
                            ReopenActionAfterFullRaise(state, seatIndex);
                        }
                    }
                    seat.HasOption = false;
                    AdvanceActorOrEndRound(state);
                    return;
                }
            default:
                throw new ArgumentOutOfRangeException(nameof(action));
        }
    }

    public static void ApplyTimeout(HoldemTableState state, int seatIndex)
    {
        var seat = state.Seats[seatIndex];
        if (seat.Status != HoldemSeatStatus.Active)
            throw new InvalidOperationException($"seat {seatIndex} not active");
        if (state.ActorIndex != seatIndex)
            throw new InvalidOperationException($"seat {seatIndex} is not the actor");

        var misses = state.ConsecutiveMisses.GetValueOrDefault(seatIndex) + 1;
        state.ConsecutiveMisses[seatIndex] = misses;

        var toCall = state.CurrentBet - seat.BetThisRound;
        if (toCall <= 0)
        {
            // Free to check — take it.
            seat.HasOption = false;
            AdvanceActorOrEndRound(state);
        }
        else
        {
            seat.Status = HoldemSeatStatus.Folded;
            seat.HasOption = false;
            AfterActionCheckForLastStanding(state);
            if (state.Phase != HoldemPhase.HandComplete) AdvanceActorOrEndRound(state);
        }

        // After the round/hand is advanced, mark the seat as sit-out if they've now hit
        // the threshold. Sit-out only takes effect at end-of-hand boundary, so the seat
        // is excluded from the next StartHand.
        if (misses >= HoldemRules.MissesBeforeSitOut && state.Phase == HoldemPhase.HandComplete)
        {
            seat.Status = HoldemSeatStatus.SittingOut;
            state.ConsecutiveMisses[seatIndex] = 0;
        }
        else if (misses >= HoldemRules.MissesBeforeSitOut)
        {
            // Even mid-hand we want the seat to skip subsequent actions this hand; folded
            // status already does that. Flag the seat to flip to SittingOut at hand end.
            // We re-check ConsecutiveMisses at StartHand and convert there.
        }
    }

    // -- Showdown + settlement ----------------------------------------------------------

    public static void ResolveShowdown(HoldemTableState state)
    {
        if (state.Phase != HoldemPhase.Showdown)
            throw new InvalidOperationException($"not at showdown (phase={state.Phase})");

        state.Pots = BuildPotStructure(state);
        DistributePots(state);
        state.Phase = HoldemPhase.HandComplete;
    }

    public static IReadOnlyList<HoldemSeatPayout> Settle(HoldemTableState state)
    {
        if (state.Phase != HoldemPhase.HandComplete)
            throw new InvalidOperationException($"not at hand-complete (phase={state.Phase})");
        // Apply payouts to chip stacks. Engine reports gross chips per Payout entry; final
        // seat stack already reflects the credit because we paid into Stack here.
        foreach (var payout in state.Payouts)
        {
            state.Seats[payout.SeatIndex].Stack += payout.Amount;
        }
        // End-of-hand: convert any seats that hit miss-threshold mid-hand to SittingOut.
        foreach (var (seat, idx) in state.Seats.Select((s, i) => (s, i)))
        {
            if (state.ConsecutiveMisses.GetValueOrDefault(idx) >= HoldemRules.MissesBeforeSitOut
                && seat.Status != HoldemSeatStatus.Empty)
            {
                seat.Status = HoldemSeatStatus.SittingOut;
                state.ConsecutiveMisses[idx] = 0;
            }
        }
        return state.Payouts;
    }

    // -- Internals ----------------------------------------------------------------------

    private static bool CanDealInto(HoldemSeatState seat) =>
        seat.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AwaitingNextHand
        && seat.Stack > 0;

    private static void PostBlind(HoldemTableState state, int seatIndex, long amount)
    {
        var seat = state.Seats[seatIndex];
        var posted = Math.Min(amount, seat.Stack);
        Commit(seat, posted);
        if (seat.Stack == 0) seat.Status = HoldemSeatStatus.AllIn;
    }

    private static void Commit(HoldemSeatState seat, long chips)
    {
        seat.Stack -= chips;
        seat.BetThisRound += chips;
        seat.TotalContribution += chips;
    }

    private static void DealHoleCards(HoldemTableState state, int firstSeat)
    {
        for (var pass = 0; pass < 2; pass++)
        {
            var idx = firstSeat;
            for (var counted = 0; counted < state.Seats.Count; counted++)
            {
                var seat = state.Seats[idx];
                if (seat.Status == HoldemSeatStatus.Active || seat.Status == HoldemSeatStatus.AllIn)
                {
                    var card = state.Deck.Draw();
                    if (pass == 0) seat.Hole1 = card;
                    else seat.Hole2 = card;
                }
                idx = (idx + 1) % state.Seats.Count;
            }
        }
    }

    private static int NextLivePlayer(HoldemTableState state, int fromIndex)
    {
        for (var step = 1; step <= state.Seats.Count; step++)
        {
            var candidate = (fromIndex + step) % state.Seats.Count;
            var seat = state.Seats[candidate];
            if (seat.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AllIn)
                return candidate;
        }
        throw new InvalidOperationException("no live players");
    }

    private static int? NextActorOrNone(HoldemTableState state, int fromIndex)
    {
        for (var step = 1; step <= state.Seats.Count; step++)
        {
            var candidate = (fromIndex + step) % state.Seats.Count;
            var seat = state.Seats[candidate];
            if (seat.Status == HoldemSeatStatus.Active && seat.HasOption)
                return candidate;
        }
        return null;
    }

    private static void ReopenActionAfterFullRaise(HoldemTableState state, int actorIndex)
    {
        // Everyone Active except the raiser must act again.
        for (var i = 0; i < state.Seats.Count; i++)
        {
            if (i == actorIndex) continue;
            var s = state.Seats[i];
            if (s.Status == HoldemSeatStatus.Active) s.HasOption = true;
        }
    }

    private static void AdvanceActorOrEndRound(HoldemTableState state)
    {
        var nextActor = NextActorOrNone(state, state.ActorIndex!.Value);
        if (nextActor is not null)
        {
            state.ActorIndex = nextActor.Value;
            return;
        }
        // No one left to act this round.
        EndBettingRound(state);
    }

    private static void EndBettingRound(HoldemTableState state)
    {
        // Sweep BetThisRound → TotalContribution is already cumulative (we add to both in
        // Commit), so just zero BetThisRound on every seat.
        foreach (var seat in state.Seats) seat.BetThisRound = 0;
        state.CurrentBet = 0;
        state.MinRaise = state.BigBlind;

        // If only one non-folded seat remains, award everything and skip directly to
        // HandComplete via the single-survivor path.
        if (CountNonFolded(state) <= 1)
        {
            BuildPotsAndAwardLastStanding(state);
            return;
        }

        // If everyone still in is all-in (or only one is active), no more betting — deal
        // remaining streets in one go and skip to showdown.
        var actionable = state.Seats.Count(s => s.Status == HoldemSeatStatus.Active);
        if (actionable <= 1)
        {
            DealRemainingStreets(state);
            state.Phase = HoldemPhase.Showdown;
            state.ActorIndex = null;
            ResolveShowdown(state);
            return;
        }

        switch (state.Phase)
        {
            case HoldemPhase.PreFlop:
                BurnAndDeal(state, 3);
                state.Phase = HoldemPhase.Flop;
                StartPostflopBetting(state);
                break;
            case HoldemPhase.Flop:
                BurnAndDeal(state, 1);
                state.Phase = HoldemPhase.Turn;
                StartPostflopBetting(state);
                break;
            case HoldemPhase.Turn:
                BurnAndDeal(state, 1);
                state.Phase = HoldemPhase.River;
                StartPostflopBetting(state);
                break;
            case HoldemPhase.River:
                state.Phase = HoldemPhase.Showdown;
                state.ActorIndex = null;
                ResolveShowdown(state);
                break;
            default:
                throw new InvalidOperationException($"unexpected phase {state.Phase}");
        }
    }

    private static void StartPostflopBetting(HoldemTableState state)
    {
        foreach (var seat in state.Seats)
        {
            if (seat.Status == HoldemSeatStatus.Active) seat.HasOption = true;
        }
        // First to act on every postflop street is the first live player left of the
        // button; if they're all-in we skip to the next.
        var first = NextLivePlayer(state, state.DealerButton);
        // Find the first seat with HasOption == true clockwise from one before `first`.
        var probeFrom = (first - 1 + state.Seats.Count) % state.Seats.Count;
        state.ActorIndex = NextActorOrNone(state, probeFrom) ?? first;
    }

    private static void BurnAndDeal(HoldemTableState state, int n)
    {
        if (state.Deck.Remaining > n) _ = state.Deck.Draw(); // burn 1
        for (var i = 0; i < n; i++) state.Board.Add(state.Deck.Draw());
    }

    private static void DealRemainingStreets(HoldemTableState state)
    {
        if (state.Phase == HoldemPhase.PreFlop) BurnAndDeal(state, 3);
        if (state.Phase is HoldemPhase.PreFlop or HoldemPhase.Flop) BurnAndDeal(state, 1);
        if (state.Phase is HoldemPhase.PreFlop or HoldemPhase.Flop or HoldemPhase.Turn) BurnAndDeal(state, 1);
    }

    private static int CountNonFolded(HoldemTableState state) =>
        state.Seats.Count(s =>
            s.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AllIn);

    private static void AfterActionCheckForLastStanding(HoldemTableState state)
    {
        // Cheap pre-check called after every Fold so we don't continue advancing actors
        // when only one seat remains.
        if (CountNonFolded(state) > 1) return;
        // Single survivor — short-circuit. Sweep BetThisRound to make Build see complete
        // contributions, build the pot, award all to the survivor, mark HandComplete.
        foreach (var seat in state.Seats) seat.BetThisRound = 0;
        BuildPotsAndAwardLastStanding(state);
    }

    private static void BuildPotsAndAwardLastStanding(HoldemTableState state)
    {
        state.Pots = BuildPotStructure(state);
        var winner = state.Seats
            .Select((s, i) => (s, i))
            .Single(t => t.s.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AllIn).i;
        var total = state.Pots.Total;
        if (total > 0)
            state.Payouts.Add(new HoldemSeatPayout(winner, total, "uncalled / last standing"));
        state.Phase = HoldemPhase.HandComplete;
        state.ActorIndex = null;
    }

    // -- Side-pot construction ----------------------------------------------------------

    internal static HoldemPotStructure BuildPotStructure(HoldemTableState state)
    {
        // Per-seat (index, contribution, folded?) for any seat that put chips in.
        var entries = state.Seats
            .Select((s, i) => (Index: i, Contribution: s.TotalContribution, Folded: s.Status == HoldemSeatStatus.Folded, NonFolded: s.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AllIn))
            .Where(e => e.Contribution > 0)
            .ToList();

        // Distinct contribution levels ascending. Each level slices off one pot layer.
        var levels = entries.Select(e => e.Contribution).Distinct().OrderBy(x => x).ToList();

        var pots = new List<HoldemPot>();
        long prev = 0;
        foreach (var level in levels)
        {
            long slice = 0;
            var eligible = new HashSet<int>();
            foreach (var e in entries)
            {
                if (e.Contribution >= level)
                {
                    slice += level - prev;
                    if (!e.Folded) eligible.Add(e.Index);
                }
            }
            if (slice > 0) pots.Add(new HoldemPot(slice, eligible));
            prev = level;
        }

        if (pots.Count == 0)
            return new HoldemPotStructure { MainPot = new HoldemPot(0, new HashSet<int>()) };

        return new HoldemPotStructure
        {
            MainPot = pots[0],
            SidePots = pots.Skip(1).ToList(),
        };
    }

    private static void DistributePots(HoldemTableState state)
    {
        if (state.Pots is null) throw new InvalidOperationException("pots not built");
        foreach (var pot in state.Pots.AllPots())
        {
            if (pot.Amount <= 0) continue;
            var contenders = pot.EligibleSeats.Where(i => state.Seats[i].Status != HoldemSeatStatus.Folded).ToList();
            if (contenders.Count == 0) continue;
            if (contenders.Count == 1)
            {
                state.Payouts.Add(new HoldemSeatPayout(contenders[0], pot.Amount, "uncontested"));
                continue;
            }

            // Evaluate each contender's best 5-of-7.
            var values = contenders
                .Select(i => (Index: i, Value: EvaluateContender(state, i)))
                .ToList();
            var best = values.Max(t => t.Value);
            var winners = values.Where(t => t.Value.CompareTo(best) == 0).Select(t => t.Index).ToList();

            var share = pot.Amount / winners.Count;
            var remainder = pot.Amount % winners.Count;

            // Odd chip(s) go to the first winner clockwise from the button.
            var winnersInOrder = new List<int>(winners.Count);
            var probe = state.DealerButton;
            while (winnersInOrder.Count < winners.Count)
            {
                probe = (probe + 1) % state.Seats.Count;
                if (winners.Contains(probe)) winnersInOrder.Add(probe);
            }

            for (var w = 0; w < winnersInOrder.Count; w++)
            {
                var amount = share + (w < remainder ? 1 : 0);
                state.Payouts.Add(new HoldemSeatPayout(winnersInOrder[w], amount, "showdown"));
            }
        }
    }

    private static HandValue EvaluateContender(HoldemTableState state, int seatIndex)
    {
        var seat = state.Seats[seatIndex];
        var cards = new List<Card>(7) { seat.Hole1!, seat.Hole2! };
        cards.AddRange(state.Board);
        return HandEvaluator.EvaluateBest(cards);
    }
}
