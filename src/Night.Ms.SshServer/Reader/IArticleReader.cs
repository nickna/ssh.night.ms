namespace Night.Ms.SshServer.Reader;

// A single hyperlink lifted out of an extracted article body. Url is always absolute and
// http(s) — relative hrefs are resolved against the article's source URL during extraction.
public sealed record ReaderLink(string Text, Uri Url);

// The reader's view of a web page: title, byline, a structured block stream for the body
// (headings, paragraphs, code, blockquotes, lists), and a deduped list of links lifted out
// of the body in DOM order. Returned by IArticleReader.ReadAsync; null means the URL
// couldn't be fetched or the page wasn't readable enough for SmartReader to extract.
//
// Anchors found inside Blocks carry a 1-based LinkIndex pointing into Links — the renderer
// appends a [N] reference next to each link so the reader can match it to the links pane.
public sealed record ReaderArticle(
    string SourceUrl,
    string? Title,
    string? Byline,
    string? SiteName,
    IReadOnlyList<ArticleBlock> Blocks,
    IReadOnlyList<ReaderLink> Links,
    int? ReadingTimeMinutes,
    DateTimeOffset? PublishedAt);

// Fetches a URL, runs Mozilla Readability over it via SmartReader, and returns a stripped-
// down article suitable for terminal rendering. Implementations are expected to enforce a
// fetch timeout and a maximum response size, and to never throw on transport/parse failure
// (return null instead) so the TUI can render a polite error.
public interface IArticleReader
{
    Task<ReaderArticle?> ReadAsync(Uri url, CancellationToken cancellationToken = default);
}
