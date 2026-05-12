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

// How aggressively to filter the source HTML before extracting blocks.
//   Reader — run Mozilla Readability (SmartReader) first, then walk the cleaned-up
//            article body. Best for prose pages (news articles, blog posts, docs);
//            strips nav/sidebar/footer/ads.
//   Raw    — skip Readability and walk the full DOM. Best for forum threads, GitHub
//            UI, dashboards, search results — pages where Readability over-strips and
//            leaves nothing behind. Renders more chrome but at least shows content.
public enum ReadMode { Reader, Raw }

// Fetches a URL and returns a stripped-down article suitable for terminal rendering.
// Implementations enforce a fetch timeout and a maximum response size, and never throw on
// transport/parse failure (return null instead) so the TUI can render a polite error.
public interface IArticleReader
{
    Task<ReaderArticle?> ReadAsync(Uri url, ReadMode mode = ReadMode.Reader, CancellationToken cancellationToken = default);
}
