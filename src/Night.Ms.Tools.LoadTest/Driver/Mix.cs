namespace Night.Ms.Tools.LoadTest.Driver;

// Proportions of chat / forum / idle bots in the assigned pool. Parsed from a CLI
// flag like "60/30/10" — values sum to 100 (or whatever) and become percentages.
// Defaults to 60/30/10 chat/forum/idle, matching the plan.
public sealed record Mix(int ChatPct, int ForumPct, int IdlePct)
{
    public static Mix Default { get; } = new(60, 30, 10);

    public static Mix Parse(string spec)
    {
        var parts = spec.Split('/', StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length != 3) throw new FormatException($"--mix expects three slash-separated integers, got '{spec}'.");
        if (!int.TryParse(parts[0], out var chat) || chat < 0) throw new FormatException("--mix chat percentage must be a non-negative integer.");
        if (!int.TryParse(parts[1], out var forum) || forum < 0) throw new FormatException("--mix forum percentage must be a non-negative integer.");
        if (!int.TryParse(parts[2], out var idle) || idle < 0) throw new FormatException("--mix idle percentage must be a non-negative integer.");
        if (chat + forum + idle == 0) throw new FormatException("--mix at least one percentage must be positive.");
        return new Mix(chat, forum, idle);
    }

    public override string ToString() => $"{ChatPct}/{ForumPct}/{IdlePct}";
}
