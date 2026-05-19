namespace Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

// Four corners (tight/loose × passive/aggressive) plus Balanced. Names are surfaced to
// humans via the CPU persona handles, so a table of CPUs feels like a mixed cast rather
// than five copies of the same robot.
//
//   Tightness:       0=any-two, 1=only-premium. Sets pre-flop entry threshold.
//   Aggression:      0=always-call, 1=always-raise-when-entering. Post-flop bet sizing.
//   BluffFrequency:  Chance to bet/raise with low equity. 0..0.2 typical.
//   ContinuationBet: Chance to c-bet the flop as pre-flop aggressor. 0..1.
public sealed record CpuPersonality(
    string Name,
    double Tightness,
    double Aggression,
    double BluffFrequency,
    double ContinuationBet);

public static class CpuPersonalities
{
    public static readonly CpuPersonality TightPassive    = new("tight-passive",    0.80, 0.20, 0.03, 0.40);
    public static readonly CpuPersonality TightAggressive = new("tight-aggressive", 0.75, 0.75, 0.08, 0.75);
    public static readonly CpuPersonality LoosePassive    = new("loose-passive",    0.45, 0.25, 0.05, 0.30);
    public static readonly CpuPersonality LooseAggressive = new("loose-aggressive", 0.40, 0.80, 0.15, 0.85);
    public static readonly CpuPersonality Balanced        = new("balanced",         0.60, 0.55, 0.07, 0.60);

    public static readonly IReadOnlyList<CpuPersonality> All = new[]
    {
        TightPassive, TightAggressive, LoosePassive, LooseAggressive, Balanced,
    };

    public static CpuPersonality? ByName(string name) =>
        All.FirstOrDefault(p => string.Equals(p.Name, name, StringComparison.OrdinalIgnoreCase));
}
