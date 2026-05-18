using Night.Ms.Tools.LoadTest.Scenarios;

namespace Night.Ms.Tools.LoadTest.Driver;

// Maps a 1-based bot index → scenario, deterministically and stably across runs.
// Layout: every bot's slot is `(index - 1) % 100`. The first ChatPct slots run chat,
// the next ForumPct slots run forum, and the remainder run idle. At any N this gives
// ≈ the requested proportions with at most 1 bot of rounding error per scenario kind.
public sealed class ProfileAssignment
{
    private readonly Func<int, IScenario> _chatFactory;
    private readonly Func<int, IScenario>? _forumFactory;
    private readonly IScenario _idleScenario;
    private readonly int _chatBuckets;
    private readonly int _forumBuckets;

    public ProfileAssignment(Mix mix, Func<int, IScenario> chatFactory, Func<int, IScenario>? forumFactory, IScenario idleScenario)
    {
        _chatFactory = chatFactory;
        _forumFactory = forumFactory;
        _idleScenario = idleScenario;

        // Normalize any total to 100 buckets so 60/30/10 and 6/3/1 produce the same
        // assignment. Idle gets the remainder so the buckets fully fill the [0, 100)
        // range — no bot falls into an unassigned slot.
        var total = Math.Max(1, mix.ChatPct + mix.ForumPct + mix.IdlePct);
        _chatBuckets = (int)Math.Round(100.0 * mix.ChatPct / total);
        _forumBuckets = (int)Math.Round(100.0 * mix.ForumPct / total);
        if (_chatBuckets + _forumBuckets > 100) _forumBuckets = 100 - _chatBuckets;
    }

    public IScenario For(int botIndex)
    {
        var bucket = (botIndex - 1) % 100;
        if (bucket < _chatBuckets) return _chatFactory(botIndex);
        if (bucket < _chatBuckets + _forumBuckets) return _forumFactory?.Invoke(botIndex) ?? _idleScenario;
        return _idleScenario;
    }
}
