using Night.Ms.SshServer.Doors.Games.Holdem.Cpu;

namespace Night.Ms.SshServer.Doors.Multiplayer;

// Concrete persona catalog. Hard-coded for v1; future games register their own personas
// per game-key. Personas are addressable by id for the bot harness and surfaced to humans
// by handle.
public sealed class CpuPersonaRegistry : ICpuPersonaRegistry
{
    private static readonly IReadOnlyList<CpuPersona> HoldemPersonas =
    [
        new("cpu:tight-tom",      "TightTom",      CpuPersonalities.TightPassive.Name),
        new("cpu:steady-stan",    "SteadyStan",    CpuPersonalities.TightAggressive.Name),
        new("cpu:loose-lucy",     "LooseLucy",     CpuPersonalities.LoosePassive.Name),
        new("cpu:wild-wendy",     "WildWendy",     CpuPersonalities.LooseAggressive.Name),
        new("cpu:balanced-bob",   "BalancedBob",   CpuPersonalities.Balanced.Name),
        new("cpu:balanced-betty", "BalancedBetty", CpuPersonalities.Balanced.Name),
    ];

    public IReadOnlyList<CpuPersona> ForGame(string gameKey) =>
        gameKey == "holdem" ? HoldemPersonas : Array.Empty<CpuPersona>();

    public CpuPersona? Get(string personaId) =>
        HoldemPersonas.FirstOrDefault(p => p.Id == personaId);
}
