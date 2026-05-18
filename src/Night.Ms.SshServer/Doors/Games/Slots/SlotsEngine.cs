namespace Night.Ms.SshServer.Doors.Games.Slots;

// Pure spin-and-evaluate logic. No DB, no UI, no DI. The whole game state for one spin is
// (rng, paytable) → SlotsResult. Splits the engine from the screen so paytable math is
// trivially unit-testable and tuning the weights doesn't require firing up Terminal.Gui.
public sealed class SlotsEngine(IGameRng rng)
{
    public SlotsResult Spin()
    {
        var r1 = DrawReel();
        var r2 = DrawReel();
        var r3 = DrawReel();
        return Evaluate(r1, r2, r3);
    }

    public static SlotsResult Evaluate(SlotSymbol r1, SlotSymbol r2, SlotSymbol r3)
    {
        // Three of a kind beats every cherry kicker. Blank-blank-blank is intentionally not
        // a paying line — it's just three losses arranged in a row.
        if (r1 == r2 && r2 == r3 && r1 != SlotSymbol.Blank
            && SlotPaytable.ThreeOfAKindMultipliers.TryGetValue(r1, out var triple))
        {
            return new SlotsResult(r1, r2, r3, triple, $"Three {r1.DisplayName()}s — {triple}×");
        }

        var cherryCount = (r1 == SlotSymbol.Cherry ? 1 : 0)
                        + (r2 == SlotSymbol.Cherry ? 1 : 0)
                        + (r3 == SlotSymbol.Cherry ? 1 : 0);

        if (cherryCount == 2)
        {
            return new SlotsResult(r1, r2, r3, SlotPaytable.TwoCherryMultiplier,
                $"Two cherries — {SlotPaytable.TwoCherryMultiplier}×");
        }

        if (cherryCount == 1 && r1 == SlotSymbol.Cherry)
        {
            return new SlotsResult(r1, r2, r3, SlotPaytable.OneCherryReelOneMultiplier,
                $"Cherry on reel 1 — {SlotPaytable.OneCherryReelOneMultiplier}× (break-even)");
        }

        return new SlotsResult(r1, r2, r3, 0, "No match");
    }

    private SlotSymbol DrawReel()
    {
        var roll = rng.Next(SlotPaytable.TotalWeight);
        var cumulative = 0;
        foreach (var (symbol, weight) in SlotPaytable.ReelWeights)
        {
            cumulative += weight;
            if (roll < cumulative) return symbol;
        }
        // Unreachable if TotalWeight == sum of weights, but if the data ever drifts return
        // Blank (the safest no-pay fallback) rather than throw — a crashed spin is worse
        // than a misweighted one.
        return SlotSymbol.Blank;
    }
}
