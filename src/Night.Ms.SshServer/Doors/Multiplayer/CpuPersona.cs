namespace Night.Ms.SshServer.Doors.Multiplayer;

// A pickable CPU identity. PolicyKey is engine-specific (Hold'em uses "tight-aggressive",
// "loose-passive", etc.); the framework treats it as an opaque string and hands it to the
// engine's strategy factory.
public sealed record CpuPersona(string Id, string Handle, string PolicyKey);

public interface ICpuPersonaRegistry
{
    IReadOnlyList<CpuPersona> ForGame(string gameKey);
    CpuPersona? Get(string personaId);
}
