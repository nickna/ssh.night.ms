using Night.Ms.Tools.LoadTest.Scenarios;

namespace Night.Ms.Tools.LoadTest.Driver;

// Maps a 1-based bot index → scenario. Phase 2 only knows IdleScenario; Phase 3/4
// extend this with chat + forum and parse a `--mix` flag here. Keeping the indirection
// now means RunCommand doesn't need to change when those land.
public sealed class ProfileAssignment
{
    private readonly IReadOnlyList<IScenario> _scenarios;

    private ProfileAssignment(IReadOnlyList<IScenario> scenarios)
    {
        _scenarios = scenarios;
    }

    public static ProfileAssignment AllIdle()
    {
        var idle = new IdleScenario();
        return new ProfileAssignment([idle]);
    }

    // Deterministic: bot index N% scenarios.Count picks the slot. Until the mix flag
    // is wired, every bot lands on _scenarios[0] (idle).
    public IScenario For(int botIndex) => _scenarios[(botIndex - 1) % _scenarios.Count];
}
