namespace Night.Ms.SshServer.Realtime;

// Unified result envelope for chat mutations. Replaces the per-method Result and
// PostResult hierarchies that each redeclared Ok/NotFound/Forbidden/Invalid.
//
// JoinResult (in ChatService) is intentionally not folded in here — its success variants
// carry a Channel and its error variants are screen-specific (e.g. UserNotFound carries a
// handle string), so the savings would be marginal at the cost of a wider blast radius.
public abstract record ChatOpResult
{
    public sealed record Ok : ChatOpResult;
    public sealed record NotFound : ChatOpResult;
    public sealed record Forbidden(string Reason) : ChatOpResult;
    public sealed record Invalid(string Reason) : ChatOpResult;
    public sealed record Posted(long MessageId, DateTimeOffset At) : ChatOpResult;

    public static readonly Ok OkInstance = new();
    public static readonly NotFound NotFoundInstance = new();
}
