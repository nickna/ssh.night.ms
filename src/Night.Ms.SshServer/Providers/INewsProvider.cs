namespace Night.Ms.SshServer.Providers;

public sealed record NewsHeadline(
    string Title,
    string? Url,
    string? Author,
    int? Score,
    DateTimeOffset PublishedAt);

public interface INewsProvider
{
    // Returns the latest headlines (most relevant first), capped to `max`. Implementations
    // are expected to cache; the TUI calls this on every NewsScreen open.
    Task<IReadOnlyList<NewsHeadline>> GetTopAsync(int max, CancellationToken cancellationToken = default);
}
