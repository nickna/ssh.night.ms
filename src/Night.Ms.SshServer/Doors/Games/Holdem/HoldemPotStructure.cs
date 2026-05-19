namespace Night.Ms.SshServer.Doors.Games.Holdem;

public sealed class HoldemPotStructure
{
    public required HoldemPot MainPot { get; init; }
    public IReadOnlyList<HoldemPot> SidePots { get; init; } = Array.Empty<HoldemPot>();

    public IEnumerable<HoldemPot> AllPots()
    {
        yield return MainPot;
        foreach (var p in SidePots) yield return p;
    }

    public long Total => AllPots().Sum(p => p.Amount);
}
