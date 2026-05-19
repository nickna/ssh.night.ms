using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Games.Common.Cards;
using Night.Ms.SshServer.Doors.Games.Holdem;
using Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

namespace Night.Ms.SshServer.Tests.Doors.Games.Holdem;

// Sanity stress test: 6 CPUs play 100 hands at a single table with a deterministic RNG.
// Asserts: chip conservation across all seats, no exceptions, no infinite loops, every
// hand reaches HandComplete. If side-pot math, action ordering, or the AI's clamping logic
// has any leaks, the chips drift and this test fails.
public class HoldemHundredHandIntegrationTests
{
    private sealed class DeterministicRng(uint seed) : IGameRng
    {
        private uint _s = seed == 0 ? 1u : seed;
        public int Next(int max) { Step(); return (int)(_s % (uint)max); }
        public int Next(int min, int max) => min + Next(max - min);
        public double NextDouble() { Step(); return _s / (double)uint.MaxValue; }
        private void Step() { _s = _s * 1664525u + 1013904223u; }
    }

    [Fact]
    public void Hundred_hands_six_cpus_chips_conserved()
    {
        var rng = new DeterministicRng(seed: 12345);
        var state = new HoldemTableState(6, smallBlind: 5, bigBlind: 10, rng);
        for (var i = 0; i < 6; i++)
        {
            state.Seats[i].Status = HoldemSeatStatus.Active;
            state.Seats[i].Stack = 1000;
        }
        var initialTotal = state.Seats.Sum(s => s.Stack);

        ICpuStrategy[] strategies =
        [
            new EquityCpuStrategy(CpuPersonalities.TightPassive),
            new EquityCpuStrategy(CpuPersonalities.TightAggressive),
            new EquityCpuStrategy(CpuPersonalities.LoosePassive),
            new EquityCpuStrategy(CpuPersonalities.LooseAggressive),
            new EquityCpuStrategy(CpuPersonalities.Balanced),
            new EquityCpuStrategy(CpuPersonalities.Balanced),
        ];

        var button = 0;
        var handsPlayed = 0;

        for (var hand = 0; hand < 100; hand++)
        {
            // Promote sat-out seats back if they still have chips (one-hand penalty).
            foreach (var s in state.Seats)
            {
                if (s.Status == HoldemSeatStatus.SittingOut && s.Stack > 0)
                    s.Status = HoldemSeatStatus.Active;
            }

            // Anyone with chips and not currently SittingOut is dealable; StartHand
            // promotes Folded/AllIn → Active.
            var dealable = state.Seats.Count(s =>
                s.Status != HoldemSeatStatus.Empty
                && s.Status != HoldemSeatStatus.SittingOut
                && s.Stack > 0);
            if (dealable < 2) break;

            // Adjust button to a seat with chips.
            for (var step = 0; step < 6; step++)
            {
                var candidate = (button + step) % 6;
                if (state.Seats[candidate].Stack > 0
                    && state.Seats[candidate].Status != HoldemSeatStatus.SittingOut)
                {
                    button = candidate;
                    break;
                }
            }

            state.RebindDeck(new Deck(rng));
            HoldemEngine.StartHand(state, button);

            var iterationGuard = 0;
            while (state.Phase != HoldemPhase.HandComplete)
            {
                if (++iterationGuard > 2000)
                    throw new InvalidOperationException(
                        $"hand {hand}: infinite loop suspected (phase={state.Phase}, actor={state.ActorIndex})");
                if (state.ActorIndex is not int actor)
                    throw new InvalidOperationException(
                        $"hand {hand}: actor missing mid-hand (phase={state.Phase})");

                var action = strategies[actor].Decide(state, actor);
                HoldemEngine.ApplyAction(state, actor, action);
            }

            HoldemEngine.Settle(state);

            var nowTotal = state.Seats.Sum(s => s.Stack);
            Assert.Equal(initialTotal, nowTotal);
            Assert.All(state.Seats, s => Assert.True(s.Stack >= 0, $"seat went negative on hand {hand}"));

            // Advance button to next live seat.
            button = (button + 1) % 6;
            handsPlayed = hand + 1;
        }

        // We don't insist on 100 — the table can collapse to heads-up and that's fine —
        // but a deterministic seed should always yield at least a handful of completed
        // hands. 25 is a generous lower bound: in practice we see ~60-90 with this seed.
        Assert.True(handsPlayed >= 25, $"only completed {handsPlayed} hands");
        // Sanity: at minimum someone should have won/lost chips. If every hand walked the
        // blinds back to a single survivor we'd see only small swings; a CPU bust-out (or
        // many) means a stack went to 0 somewhere along the way.
        var anyMoved = state.Seats.Any(s => s.Stack != 1000);
        Assert.True(anyMoved, "no chips ever moved across 25+ hands");
    }
}
