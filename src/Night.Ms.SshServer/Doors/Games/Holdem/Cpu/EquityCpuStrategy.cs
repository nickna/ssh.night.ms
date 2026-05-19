using Night.Ms.SshServer.Doors.Games.Common.Cards;

namespace Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

// Pre-flop: Chen score gated by personality thresholds.
// Post-flop: Monte Carlo equity vs. potOdds, modulated by personality bet sizing + bluff.
//
// Stateless; the engine asks Decide(state, seatIndex) once per turn. All randomness flows
// through state.Rng so tests can seed deterministically.
public sealed class EquityCpuStrategy(CpuPersonality personality) : ICpuStrategy
{
    private readonly CpuPersonality _p = personality;

    // Smaller sim count for early decisions (more cards to vary), larger for the river
    // when only the showdown matters. Empirically these are enough to give consistent
    // equity estimates without burning CPU.
    private static int SimulationsForPhase(HoldemPhase phase) => phase switch
    {
        HoldemPhase.PreFlop => 0,   // not used pre-flop; Chen handles it
        HoldemPhase.Flop => 600,
        HoldemPhase.Turn => 800,
        HoldemPhase.River => 1000,
        _ => 500,
    };

    public HoldemAction Decide(HoldemTableState state, int seatIndex)
    {
        var legal = HoldemEngine.LegalActions(state, seatIndex);
        if (legal.Count == 0) return HoldemAction.Default();

        var seat = state.Seats[seatIndex];
        var toCall = state.CurrentBet - seat.BetThisRound;

        return state.Phase == HoldemPhase.PreFlop
            ? DecidePreflop(state, seat, legal, toCall)
            : DecidePostflop(state, seatIndex, seat, legal, toCall);
    }

    // -- Pre-flop -----------------------------------------------------------------------

    private HoldemAction DecidePreflop(
        HoldemTableState state, HoldemSeatState seat, IReadOnlyList<HoldemAction> legal, long toCall)
    {
        var chen = PreflopRanges.ChenScore(seat.Hole1!, seat.Hole2!);

        // Tightness 0 → playThreshold 2 (any-two), Tightness 1 → playThreshold 10 (only
        // premium). Aggression 0 → raise needs +5 above play, Aggression 1 → raise +1.
        var playThreshold = Lerp(2, 10, _p.Tightness);
        var raiseThreshold = playThreshold + Lerp(1, 5, 1.0 - _p.Aggression);

        if (chen < playThreshold)
        {
            // Too weak to enter. Fold if facing a bet, check if free.
            if (toCall <= 0 && HasKind(legal, HoldemActionKind.Check)) return HoldemAction.Check();
            return HoldemAction.Fold();
        }

        if (chen >= raiseThreshold)
        {
            // Open-raise or 3-bet to 3 BB. Clamp to legal range.
            var openTo = state.BigBlind * 3;
            if (HasKind(legal, HoldemActionKind.Raise))
            {
                var clamped = ClampRaise(state, seat, openTo);
                return HoldemAction.Raise(clamped);
            }
            if (HasKind(legal, HoldemActionKind.AllIn)) return HoldemAction.AllIn();
        }

        // Limp-call territory: enter the pot for the minimum.
        if (toCall <= 0 && HasKind(legal, HoldemActionKind.Check)) return HoldemAction.Check();
        if (HasKind(legal, HoldemActionKind.Call)) return HoldemAction.Call();
        return HoldemAction.Fold();
    }

    // -- Post-flop ----------------------------------------------------------------------

    private HoldemAction DecidePostflop(
        HoldemTableState state, int seatIndex, HoldemSeatState seat, IReadOnlyList<HoldemAction> legal, long toCall)
    {
        var hole = new[] { seat.Hole1!, seat.Hole2! };
        var opponents = state.Seats.Count(s =>
            s.Status is HoldemSeatStatus.Active or HoldemSeatStatus.AllIn) - 1;
        if (opponents < 1) opponents = 1;

        var equity = MonteCarloEquity.Estimate(
            hole, state.Board, opponents, SimulationsForPhase(state.Phase), state.Rng);

        var potTotal = state.Seats.Sum(s => s.TotalContribution);
        var potOdds = toCall <= 0 ? 0 : (double)toCall / (potTotal + toCall);

        var bluff = state.Rng.NextDouble() < _p.BluffFrequency;

        // Strong made hand: bet/raise pot-sized; near-lock equity goes all-in.
        if (equity > 0.85)
        {
            if (equity > 0.95 && _p.Aggression > 0.6 && HasKind(legal, HoldemActionKind.AllIn))
                return HoldemAction.AllIn();
            return RaiseOrBet(state, seat, legal, potTotal, sizeFraction: 1.0);
        }

        if (equity > 0.65)
        {
            if (_p.Aggression > 0.5)
                return RaiseOrBet(state, seat, legal, potTotal, sizeFraction: 0.5);
            return Passive(legal, toCall);
        }

        if (equity > 0.50) return Passive(legal, toCall);

        // Marginal: call only if pot odds justify it.
        if (equity > potOdds + 0.02)
        {
            if (toCall <= 0 && HasKind(legal, HoldemActionKind.Check)) return HoldemAction.Check();
            if (HasKind(legal, HoldemActionKind.Call)) return HoldemAction.Call();
        }

        if (bluff && toCall <= 0 && HasKind(legal, HoldemActionKind.Bet))
            return RaiseOrBet(state, seat, legal, potTotal, sizeFraction: 0.5);

        if (toCall <= 0 && HasKind(legal, HoldemActionKind.Check)) return HoldemAction.Check();
        return HoldemAction.Fold();
    }

    private static HoldemAction Passive(IReadOnlyList<HoldemAction> legal, long toCall)
    {
        if (toCall <= 0 && HasKind(legal, HoldemActionKind.Check)) return HoldemAction.Check();
        if (HasKind(legal, HoldemActionKind.Call)) return HoldemAction.Call();
        return HoldemAction.Fold();
    }

    private static HoldemAction RaiseOrBet(
        HoldemTableState state, HoldemSeatState seat, IReadOnlyList<HoldemAction> legal,
        long potTotal, double sizeFraction)
    {
        var target = (long)Math.Max(state.BigBlind, potTotal * sizeFraction);
        if (state.CurrentBet == 0 && HasKind(legal, HoldemActionKind.Bet))
        {
            var clamped = Math.Min(target, seat.Stack);
            if (clamped < state.BigBlind) return PassiveFallback(legal, state, seat);
            return HoldemAction.Bet(clamped);
        }
        if (HasKind(legal, HoldemActionKind.Raise))
        {
            var raiseTo = state.CurrentBet + target;
            var clamped = ClampRaise(state, seat, raiseTo);
            return HoldemAction.Raise(clamped);
        }
        if (HasKind(legal, HoldemActionKind.AllIn)) return HoldemAction.AllIn();
        return PassiveFallback(legal, state, seat);
    }

    private static HoldemAction PassiveFallback(IReadOnlyList<HoldemAction> legal, HoldemTableState state, HoldemSeatState seat)
    {
        var toCall = state.CurrentBet - seat.BetThisRound;
        if (toCall <= 0 && HasKind(legal, HoldemActionKind.Check)) return HoldemAction.Check();
        if (HasKind(legal, HoldemActionKind.Call)) return HoldemAction.Call();
        return HoldemAction.Fold();
    }

    // Constrain a "raise to" target to the legal min/max range for this seat. Returns the
    // closest legal target; if the seat can't make even a min-raise, drops to all-in.
    private static long ClampRaise(HoldemTableState state, HoldemSeatState seat, long target)
    {
        var minRaiseTo = state.CurrentBet + state.MinRaise;
        var maxRaiseTo = seat.BetThisRound + seat.Stack;
        if (target < minRaiseTo) target = minRaiseTo;
        if (target > maxRaiseTo) target = maxRaiseTo;
        return target;
    }

    private static bool HasKind(IReadOnlyList<HoldemAction> legal, HoldemActionKind kind)
    {
        for (var i = 0; i < legal.Count; i++) if (legal[i].Kind == kind) return true;
        return false;
    }

    private static double Lerp(double a, double b, double t) => a + (b - a) * t;
}
