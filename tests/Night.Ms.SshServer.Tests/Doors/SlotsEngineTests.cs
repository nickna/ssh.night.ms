using Night.Ms.SshServer.Doors;
using Night.Ms.SshServer.Doors.Games.Slots;

namespace Night.Ms.SshServer.Tests.Doors;

public class SlotsEngineTests
{
    // Evaluate is pure — the tests below feed reel symbols directly and skip the RNG, so
    // they exercise the paytable rules without any randomness involved.

    [Theory]
    [InlineData(SlotSymbol.Seven, 500)]
    [InlineData(SlotSymbol.Bar, 200)]
    [InlineData(SlotSymbol.Bell, 60)]
    [InlineData(SlotSymbol.Plum, 30)]
    [InlineData(SlotSymbol.Lemon, 12)]
    [InlineData(SlotSymbol.Cherry, 8)]
    public void ThreeOfAKind_pays_paytable_multiplier(SlotSymbol s, int expected)
    {
        var result = SlotsEngine.Evaluate(s, s, s);
        Assert.Equal(expected, result.Multiplier);
        Assert.Equal(50 * expected, result.Payout(50));
    }

    [Fact]
    public void ThreeBlanks_pay_nothing()
    {
        var result = SlotsEngine.Evaluate(SlotSymbol.Blank, SlotSymbol.Blank, SlotSymbol.Blank);
        Assert.Equal(0, result.Multiplier);
    }

    [Theory]
    [InlineData(SlotSymbol.Cherry, SlotSymbol.Cherry, SlotSymbol.Blank)]
    [InlineData(SlotSymbol.Cherry, SlotSymbol.Blank, SlotSymbol.Cherry)]
    [InlineData(SlotSymbol.Blank, SlotSymbol.Cherry, SlotSymbol.Cherry)]
    public void TwoCherries_anywhere_pays_two_cherry_multiplier(SlotSymbol a, SlotSymbol b, SlotSymbol c)
    {
        var result = SlotsEngine.Evaluate(a, b, c);
        Assert.Equal(SlotPaytable.TwoCherryMultiplier, result.Multiplier);
    }

    [Fact]
    public void OneCherry_on_reel_one_pays_break_even()
    {
        var result = SlotsEngine.Evaluate(SlotSymbol.Cherry, SlotSymbol.Blank, SlotSymbol.Lemon);
        Assert.Equal(SlotPaytable.OneCherryReelOneMultiplier, result.Multiplier);
    }

    [Fact]
    public void OneCherry_not_on_reel_one_does_not_pay()
    {
        var result = SlotsEngine.Evaluate(SlotSymbol.Lemon, SlotSymbol.Cherry, SlotSymbol.Blank);
        Assert.Equal(0, result.Multiplier);
    }

    [Fact]
    public void ThreeCherries_take_precedence_over_two_cherry_payout()
    {
        var result = SlotsEngine.Evaluate(SlotSymbol.Cherry, SlotSymbol.Cherry, SlotSymbol.Cherry);
        Assert.Equal(SlotPaytable.ThreeOfAKindMultipliers[SlotSymbol.Cherry], result.Multiplier);
    }

    [Fact]
    public void Spin_with_deterministic_rng_picks_expected_symbols()
    {
        // ReelWeights are sorted Seven(1), Bar(2), Bell(4), Plum(6), Lemon(10), Cherry(15), Blank(25)
        // with cumulative bands 0, 1, 3, 7, 13, 23, 38, 63. Feed rolls that land in each band.
        var rng = new SequenceRng([0, 2, 5]);
        var engine = new SlotsEngine(rng);
        var result = engine.Spin();
        Assert.Equal(SlotSymbol.Seven, result.Reel1);
        Assert.Equal(SlotSymbol.Bar, result.Reel2);
        Assert.Equal(SlotSymbol.Bell, result.Reel3);
    }

    [Fact]
    public void Weights_sum_matches_declared_TotalWeight()
    {
        Assert.Equal(SlotPaytable.TotalWeight, SlotPaytable.ReelWeights.Values.Sum());
    }

    private sealed class SequenceRng(int[] values) : IGameRng
    {
        private int _i;
        public int Next(int maxExclusive) => values[_i++];
        public int Next(int minInclusive, int maxExclusive) => values[_i++];
        public double NextDouble() => 0.5;
    }
}
