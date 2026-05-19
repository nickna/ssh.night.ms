using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem;

namespace Night.Ms.SshServer.Tests.Doors.Games.Holdem;

public class HoldemEngineTests
{
    // Tiny deterministic RNG so the engine produces the same shuffle/deal across runs. Not
    // used where tests stack the deck directly; only matters for tests that observe the
    // post-shuffle ordering.
    private sealed class DeterministicRng(uint seed) : IGameRng
    {
        private uint _s = seed == 0 ? 1u : seed;
        public int Next(int max) { Step(); return (int)(_s % (uint)max); }
        public int Next(int min, int max) => min + Next(max - min);
        public double NextDouble() { Step(); return _s / (double)uint.MaxValue; }
        private void Step() { _s = _s * 1664525u + 1013904223u; }
    }

    private static HoldemTableState NewState(int seats, long sb = 5, long bb = 10, uint rngSeed = 42)
    {
        var state = new HoldemTableState(seats, sb, bb, new DeterministicRng(rngSeed));
        for (var i = 0; i < seats; i++)
        {
            state.Seats[i].Status = HoldemSeatStatus.Active;
            state.Seats[i].Stack = 1000;
        }
        return state;
    }

    [Fact]
    public void StartHand_heads_up_dealer_posts_small_blind()
    {
        var state = NewState(2);
        HoldemEngine.StartHand(state, dealerButtonIndex: 0);
        Assert.Equal(5, state.Seats[0].BetThisRound);   // dealer = SB
        Assert.Equal(10, state.Seats[1].BetThisRound);  // BB
        // First to act preflop in heads-up: dealer (SB) acts first preflop.
        Assert.Equal(0, state.ActorIndex);
    }

    [Fact]
    public void StartHand_six_max_blinds_left_of_button()
    {
        var state = NewState(6);
        HoldemEngine.StartHand(state, dealerButtonIndex: 2);
        Assert.Equal(0, state.Seats[2].BetThisRound);   // button posts nothing
        Assert.Equal(5, state.Seats[3].BetThisRound);   // SB
        Assert.Equal(10, state.Seats[4].BetThisRound);  // BB
        Assert.Equal(5, state.ActorIndex);              // UTG = seat after BB
    }

    [Fact]
    public void LegalActions_empty_for_non_actor()
    {
        var state = NewState(3);
        HoldemEngine.StartHand(state, 0);
        var notActor = (state.ActorIndex!.Value + 1) % 3;
        Assert.Empty(HoldemEngine.LegalActions(state, notActor));
    }

    [Fact]
    public void LegalActions_preflop_utg_has_fold_call_raise_allin()
    {
        var state = NewState(3);
        HoldemEngine.StartHand(state, 0);
        var kinds = HoldemEngine.LegalActions(state, state.ActorIndex!.Value).Select(a => a.Kind).ToHashSet();
        Assert.Contains(HoldemActionKind.Fold, kinds);
        Assert.Contains(HoldemActionKind.Call, kinds);
        Assert.Contains(HoldemActionKind.Raise, kinds);
        Assert.Contains(HoldemActionKind.AllIn, kinds);
        Assert.DoesNotContain(HoldemActionKind.Check, kinds);
        Assert.DoesNotContain(HoldemActionKind.Bet, kinds);
    }

    [Fact]
    public void ApplyAction_single_survivor_short_circuits_to_hand_complete()
    {
        var state = NewState(3);
        HoldemEngine.StartHand(state, 0);
        // UTG folds, then SB folds — BB is the last standing and wins everything.
        HoldemEngine.ApplyAction(state, state.ActorIndex!.Value, HoldemAction.Fold());
        // After UTG fold, actor advances to SB (seat 1).
        HoldemEngine.ApplyAction(state, state.ActorIndex!.Value, HoldemAction.Fold());
        Assert.Equal(HoldemPhase.HandComplete, state.Phase);
        // BB is at index 2 in a 3-handed game starting at button=0 (SB=1, BB=2).
        Assert.Single(state.Payouts);
        Assert.Equal(2, state.Payouts[0].SeatIndex);
        Assert.Equal(15, state.Payouts[0].Amount);   // SB 5 + BB 10
    }

    [Fact]
    public void Raise_full_raise_reopens_action_for_already_acted_seats()
    {
        var state = NewState(3);
        HoldemEngine.StartHand(state, 0);
        // UTG calls, SB calls, BB now has option to check or raise. Have BB raise.
        HoldemEngine.ApplyAction(state, state.ActorIndex!.Value, HoldemAction.Call());
        HoldemEngine.ApplyAction(state, state.ActorIndex!.Value, HoldemAction.Call());
        // Now BB to act.
        Assert.Equal(2, state.ActorIndex);
        HoldemEngine.ApplyAction(state, 2, HoldemAction.Raise(30));   // raise to 30 (full raise increment 20)

        // UTG (seat 0) should be the actor again (clockwise from BB), and they should still
        // have HasOption = true so they're forced to act on the raise.
        Assert.True(state.Seats[0].HasOption);
        Assert.True(state.Seats[1].HasOption);
        Assert.Equal(0, state.ActorIndex);
        Assert.Equal(30, state.CurrentBet);
    }

    [Fact]
    public void Timeout_with_no_bet_to_face_resolves_to_check()
    {
        var state = NewState(3);
        HoldemEngine.StartHand(state, 0);
        // UTG calls, SB calls — BB to act with the option to check.
        HoldemEngine.ApplyAction(state, state.ActorIndex!.Value, HoldemAction.Call());
        HoldemEngine.ApplyAction(state, state.ActorIndex!.Value, HoldemAction.Call());
        Assert.Equal(2, state.ActorIndex);
        var bbSeat = state.Seats[2];
        var stackBefore = bbSeat.Stack;

        HoldemEngine.ApplyTimeout(state, 2);

        Assert.Equal(stackBefore, bbSeat.Stack);                 // no chips moved on auto-check
        Assert.NotEqual(HoldemSeatStatus.Folded, bbSeat.Status);
        Assert.Equal(HoldemPhase.Flop, state.Phase);             // betting round closed, flop dealt
    }

    [Fact]
    public void Timeout_facing_bet_folds()
    {
        var state = NewState(3);
        HoldemEngine.StartHand(state, 0);
        // UTG times out preflop facing the BB.
        var utg = state.ActorIndex!.Value;
        HoldemEngine.ApplyTimeout(state, utg);
        Assert.Equal(HoldemSeatStatus.Folded, state.Seats[utg].Status);
    }

    [Fact]
    public void ThreeConsecutiveMisses_flags_seat_for_sit_out_at_hand_complete()
    {
        // 3-handed, button=0 ⇒ SB=1, BB=2, UTG=0. Each hand seat 0 (UTG) times-out-folds
        // facing the BB. After three of those, seat 0 should be SittingOut.
        var state = NewState(3);
        for (var hand = 1; hand <= 3; hand++)
        {
            foreach (var s in state.Seats)
            {
                if (s.Status != HoldemSeatStatus.SittingOut)
                    s.Status = HoldemSeatStatus.Active;
            }
            HoldemEngine.StartHand(state, dealerButtonIndex: 0);    // UTG = seat 0
            if (state.ActorIndex == 0)
            {
                HoldemEngine.ApplyTimeout(state, 0);                // seat 0 folds
            }
            // Drive the rest of the hand to completion: each remaining seat folds in turn
            // until single-survivor short-circuit.
            while (state.Phase != HoldemPhase.HandComplete)
            {
                var actor = state.ActorIndex!.Value;
                HoldemEngine.ApplyAction(state, actor, HoldemAction.Fold());
            }
            HoldemEngine.Settle(state);
        }

        Assert.Equal(HoldemSeatStatus.SittingOut, state.Seats[0].Status);
    }
}
