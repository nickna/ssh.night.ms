using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Games.Holdem;

namespace Night.Ms.SshServer.Tests.Doors.Games.Holdem;

public class HoldemSidePotTests
{
    private static HoldemTableState BuildState(params (long Contribution, HoldemSeatStatus Status)[] seats)
    {
        var state = new HoldemTableState(seatCount: seats.Length, smallBlind: 5, bigBlind: 10, rng: new CryptoGameRng());
        for (var i = 0; i < seats.Length; i++)
        {
            state.Seats[i].Status = seats[i].Status;
            state.Seats[i].TotalContribution = seats[i].Contribution;
        }
        return state;
    }

    [Fact]
    public void WorkedExample_two_all_ins_produce_three_pots()
    {
        // [100, 100, 50 (all-in), 200, 200]
        var state = BuildState(
            (100, HoldemSeatStatus.Folded),     // seat 0 folded after putting 100 in
            (100, HoldemSeatStatus.Folded),     // seat 1 folded after putting 100 in
            ( 50, HoldemSeatStatus.AllIn),      // seat 2 short all-in for 50
            (200, HoldemSeatStatus.Active),     // seat 3 still in for 200
            (200, HoldemSeatStatus.AllIn));     // seat 4 all-in for 200

        var pots = HoldemEngine.BuildPotStructure(state);

        Assert.Equal(3, pots.AllPots().Count());
        Assert.Equal(250, pots.MainPot.Amount);                          // 50 × 5
        Assert.Equal(new HashSet<int> { 2, 3, 4 }, pots.MainPot.EligibleSeats);

        Assert.Equal(200, pots.SidePots[0].Amount);                      // (100-50) × 4 = 200
        Assert.Equal(new HashSet<int> { 3, 4 }, pots.SidePots[0].EligibleSeats);

        Assert.Equal(200, pots.SidePots[1].Amount);                      // (200-100) × 2 = 200
        Assert.Equal(new HashSet<int> { 3, 4 }, pots.SidePots[1].EligibleSeats);

        Assert.Equal(650, pots.Total);
    }

    [Fact]
    public void SinglePot_when_all_contribute_equally()
    {
        var state = BuildState(
            (10, HoldemSeatStatus.Active),
            (10, HoldemSeatStatus.Active),
            (10, HoldemSeatStatus.Folded));
        var pots = HoldemEngine.BuildPotStructure(state);
        Assert.Single(pots.AllPots());
        Assert.Equal(30, pots.MainPot.Amount);
        Assert.Equal(new HashSet<int> { 0, 1 }, pots.MainPot.EligibleSeats);
    }

    [Fact]
    public void EmptyPot_when_no_one_contributed()
    {
        var state = BuildState(
            (0, HoldemSeatStatus.Empty),
            (0, HoldemSeatStatus.Empty));
        var pots = HoldemEngine.BuildPotStructure(state);
        Assert.Equal(0, pots.Total);
    }

    [Fact]
    public void FoldedAfterAllOthersAllIn_still_contributes_chips_to_main_pot()
    {
        // Folded seat still contributed; their chips stay in the pot but they're not
        // eligible to win any of it.
        var state = BuildState(
            (20, HoldemSeatStatus.Folded),
            (20, HoldemSeatStatus.AllIn),
            (20, HoldemSeatStatus.AllIn));
        var pots = HoldemEngine.BuildPotStructure(state);
        Assert.Single(pots.AllPots());
        Assert.Equal(60, pots.MainPot.Amount);
        Assert.Equal(new HashSet<int> { 1, 2 }, pots.MainPot.EligibleSeats);
    }
}
