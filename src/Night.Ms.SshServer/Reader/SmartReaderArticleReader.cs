using System.Text;
using AngleSharp.Html.Parser;

namespace Night.Ms.SshServer.Reader;

// SmartReader-backed IArticleReader. Fetches the URL with a capped reader (so a hostile or
// huge page can't pin server memory), then hands the HTML + URL to SmartReader.ParseArticle
// for Mozilla-Readability extraction. The extracted Article.Content HTML is then walked by
// HtmlBlockExtractor into a typed block stream (headings, paragraphs, code, blockquotes,
// lists) plus a deduped, DOM-ordered link list for the TUI's links pane.
//
// Failure modes (timeout, non-200, parse error, unreadable page) all return null — the
// ReaderScreen renders an "extraction failed" state rather than crashing the session.
public sealed class SmartReaderArticleReader(
    IHttpClientFactory httpClientFactory,
    ILogger<SmartReaderArticleReader> logger)
    : IArticleReader
{
    public const string HttpClientName = "article-reader";

    private const int MaxBytes = 4 * 1024 * 1024;
    private static readonly TimeSpan FetchTimeout = TimeSpan.FromSeconds(12);

    public async Task<ReaderArticle?> ReadAsync(Uri url, ReadMode mode = ReadMode.Reader, CancellationToken cancellationToken = default)
    {
        if (url.Scheme != Uri.UriSchemeHttp && url.Scheme != Uri.UriSchemeHttps)
        {
            return null;
        }

        var html = await TryDownloadAsync(url, cancellationToken).ConfigureAwait(false);
        if (html is null)
        {
            return null;
        }

        try
        {
            var result = mode == ReadMode.Reader
                ? ParseAsReader(url, html)
                : ParseAsRaw(url, html);
            if (result is not null)
            {
                var imageCount = result.Blocks.Count(b => b is ImageBlock);
                logger.LogInformation(
                    "Article extracted: {BlockCount} blocks, {ImageCount} images, {LinkCount} links (mode={Mode}): {Url}",
                    result.Blocks.Count, imageCount, result.Links.Count, mode, url);
            }
            return result;
        }
        catch (Exception ex)
        {
            logger.LogInformation(ex, "Article parse failed (mode={Mode}): {Url}", mode, url);
            return null;
        }
    }

    private static ReaderArticle? ParseAsReader(Uri url, string html)
    {
        var article = SmartReader.Reader.ParseArticle(url.ToString(), text: html);
        if (article is null || string.IsNullOrWhiteSpace(article.Content))
        {
            return null;
        }

        var doc = new HtmlParser().ParseDocument(article.Content);
        var (blocks, links) = HtmlBlockExtractor.Extract(doc, url);
        if (blocks.Count == 0)
        {
            return null;
        }

        return new ReaderArticle(
            SourceUrl: url.ToString(),
            Title: NullIfBlank(article.Title),
            Byline: NullIfBlank(article.Byline ?? article.Author),
            SiteName: NullIfBlank(article.SiteName),
            Blocks: blocks,
            Links: links,
            ReadingTimeMinutes: article.TimeToRead.TotalMinutes >= 1
                ? (int)Math.Ceiling(article.TimeToRead.TotalMinutes)
                : null,
            PublishedAt: article.PublicationDate is { } d
                ? new DateTimeOffset(d.Ticks, TimeSpan.Zero)
                : null);
    }

    private static ReaderArticle? ParseAsRaw(Uri url, string html)
    {
        // Skip Readability — feed the full HTML to AngleSharp directly. Useful for forum
        // threads, GitHub repo views, search results, and other pages where Readability's
        // strip-to-prose heuristic leaves nothing meaningful behind.
        var doc = new HtmlParser().ParseDocument(html);
        var (blocks, links) = HtmlBlockExtractor.Extract(doc, url);
        if (blocks.Count == 0)
        {
            return null;
        }

        return new ReaderArticle(
            SourceUrl: url.ToString(),
            Title: NullIfBlank(doc.Title),
            Byline: null,
            SiteName: url.Host,
            Blocks: blocks,
            Links: links,
            ReadingTimeMinutes: null,
            PublishedAt: null);
    }

    private async Task<string?> TryDownloadAsync(Uri url, CancellationToken cancellationToken)
    {
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);

            using var cts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
            cts.CancelAfter(FetchTimeout);

            using var resp = await http.GetAsync(url, HttpCompletionOption.ResponseHeadersRead, cts.Token).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode)
            {
                logger.LogInformation("Article fetch returned {Status}: {Url}", (int)resp.StatusCode, url);
                return null;
            }

            // Skip non-HTML responses up front — Readability can't help with PDFs, video, etc.
            var mediaType = resp.Content.Headers.ContentType?.MediaType;
            if (mediaType is not null
                && !mediaType.Contains("html", StringComparison.OrdinalIgnoreCase)
                && !mediaType.Contains("xml", StringComparison.OrdinalIgnoreCase)
                && !mediaType.Contains("text/plain", StringComparison.OrdinalIgnoreCase))
            {
                logger.LogInformation("Article fetch skipped — content-type {Media}: {Url}", mediaType, url);
                return null;
            }

            await using var stream = await resp.Content.ReadAsStreamAsync(cts.Token).ConfigureAwait(false);
            using var ms = new MemoryStream(capacity: 64 * 1024);
            var buffer = new byte[16 * 1024];
            int read;
            while ((read = await stream.ReadAsync(buffer.AsMemory(), cts.Token).ConfigureAwait(false)) > 0)
            {
                var remaining = MaxBytes - (int)ms.Length;
                if (remaining <= 0)
                {
                    logger.LogInformation("Article exceeded {Max} bytes, truncating: {Url}", MaxBytes, url);
                    break;
                }
                ms.Write(buffer, 0, Math.Min(read, remaining));
            }

            var encoding = TryGetEncoding(resp.Content.Headers.ContentType?.CharSet) ?? Encoding.UTF8;
            return encoding.GetString(ms.GetBuffer(), 0, (int)ms.Length);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            logger.LogInformation("Article fetch timed out after {Timeout}s: {Url}", FetchTimeout.TotalSeconds, url);
            return null;
        }
        catch (Exception ex)
        {
            logger.LogInformation(ex, "Article fetch failed: {Url}", url);
            return null;
        }
    }

    private static Encoding? TryGetEncoding(string? charset)
    {
        if (string.IsNullOrWhiteSpace(charset)) return null;
        try { return Encoding.GetEncoding(charset); } catch { return null; }
    }

    private static string? NullIfBlank(string? s) => string.IsNullOrWhiteSpace(s) ? null : s.Trim();
}
