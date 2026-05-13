using System.Text.RegularExpressions;

namespace Night.Ms.SshServer.Tui.Chat;

// Extracts http(s) image URLs from a chat message body. We deliberately match by file
// extension instead of probing the URL, because (a) it's stable and obvious to the author
// ("post a .png, get an inline render"), and (b) it avoids accidental fetches of arbitrary
// links pasted into chat. Query strings are kept (some CDNs require them); fragments are
// dropped because no image server cares about #anchor.
//
// The regex is conservative: only the common raster formats, trailing common punctuation
// (',', '.', ')', ']', '!', '?') is stripped so "see https://x.com/foo.png." resolves cleanly.
internal static class UrlExtractor
{
    private static readonly Regex ImageUrl = new(
        @"\bhttps?://[^\s<>""]+?\.(?:png|jpe?g|gif|webp|bmp)(?:\?[^\s<>""]*)?",
        RegexOptions.IgnoreCase | RegexOptions.Compiled);

    public static IReadOnlyList<Uri> FindImageUrls(string body)
    {
        if (string.IsNullOrEmpty(body)) return Array.Empty<Uri>();

        var hits = new List<Uri>();
        var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (Match m in ImageUrl.Matches(body))
        {
            var trimmed = TrimTrailingPunctuation(m.Value);
            if (!Uri.TryCreate(trimmed, UriKind.Absolute, out var uri)) continue;
            if (uri.Scheme is not "http" and not "https") continue;
            if (!seen.Add(uri.AbsoluteUri)) continue; // de-dup repeats in a single message
            hits.Add(uri);
        }
        return hits;
    }

    // Strips sentence-ending punctuation that the regex greedy-match swallowed but doesn't
    // belong to the URL: "see foo.png." → "foo.png". We don't strip closing parens
    // categorically because some real URLs contain them (Wikipedia); only at the very end.
    private static string TrimTrailingPunctuation(string url)
    {
        var end = url.Length;
        while (end > 0)
        {
            var c = url[end - 1];
            if (c is '.' or ',' or '!' or '?' or ';' or ':' or ')' or ']' or '>' or '"' or '\'')
            {
                end--;
                continue;
            }
            break;
        }
        return url.Substring(0, end);
    }
}
