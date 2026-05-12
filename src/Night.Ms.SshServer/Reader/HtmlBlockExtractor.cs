using AngleSharp.Dom;
using AngleSharp.Html.Dom;

namespace Night.Ms.SshServer.Reader;

// Walks the SmartReader-extracted article DOM and produces a flat list of ArticleBlocks
// plus a parallel list of ReaderLinks. Anchors are numbered in DOM order (1-based) and
// the run that emits an anchor's text also carries its LinkIndex, so the renderer can
// append a [N] reference next to the link text and the links pane can show "[N] text".
//
// This is intentionally a small subset of HTML — the goal is readable prose, not pixel-
// perfect rendering. Tables, forms, scripts, embedded media, etc. are ignored: we recurse
// into unknown elements and pick up their text where it makes sense.
internal static class HtmlBlockExtractor
{
    public static (IReadOnlyList<ArticleBlock> Blocks, IReadOnlyList<ReaderLink> Links) Extract(
        IHtmlDocument doc,
        Uri baseUrl)
    {
        var blocks = new List<ArticleBlock>();
        var links = new List<ReaderLink>();
        var seenLinkUrls = new Dictionary<string, int>(StringComparer.OrdinalIgnoreCase);
        var ctx = new ExtractCtx(baseUrl, links, seenLinkUrls);

        var root = (INode?)doc.Body ?? doc;
        WalkBlocks(root, blocks, ctx);
        return (blocks, links);
    }

    private sealed record ExtractCtx(Uri BaseUrl, List<ReaderLink> Links, Dictionary<string, int> SeenLinkUrls);

    private static void WalkBlocks(INode parent, List<ArticleBlock> output, ExtractCtx ctx)
    {
        foreach (var node in parent.ChildNodes)
        {
            if (node is not IElement el)
            {
                // Loose text nodes at block level — wrap into a paragraph if they carry content.
                if (node is IText t)
                {
                    var trimmed = t.TextContent.Trim();
                    if (trimmed.Length > 0)
                    {
                        output.Add(new ParagraphBlock(new[] { new Run(trimmed) }));
                    }
                }
                continue;
            }

            switch (el.TagName)
            {
                case "H1": case "H2": case "H3": case "H4": case "H5": case "H6":
                    var level = el.TagName[1] - '0';
                    var runs = CollectInline(el, RunStyle.Heading, ctx);
                    if (runs.Count > 0) output.Add(new HeadingBlock(level, runs));
                    break;

                case "P":
                    var pRuns = CollectInline(el, RunStyle.Plain, ctx);
                    if (pRuns.Count > 0) output.Add(new ParagraphBlock(pRuns));
                    break;

                case "PRE":
                    var code = el.TextContent.Replace("\r\n", "\n").TrimEnd('\n');
                    if (code.Length > 0) output.Add(new CodeBlock(code));
                    break;

                case "BLOCKQUOTE":
                    var inner = new List<ArticleBlock>();
                    WalkBlocks(el, inner, ctx);
                    if (inner.Count == 0)
                    {
                        var txt = CollectInline(el, RunStyle.Plain, ctx);
                        if (txt.Count > 0) inner.Add(new ParagraphBlock(txt));
                    }
                    if (inner.Count > 0) output.Add(new BlockquoteBlock(inner));
                    break;

                case "UL":
                    foreach (var li in DirectChildren(el, "LI"))
                    {
                        var liRuns = CollectInline(li, RunStyle.Plain, ctx);
                        if (liRuns.Count > 0) output.Add(new ListItemBlock("• ", liRuns));
                    }
                    break;

                case "OL":
                    var n = 1;
                    foreach (var li in DirectChildren(el, "LI"))
                    {
                        var liRuns = CollectInline(li, RunStyle.Plain, ctx);
                        if (liRuns.Count > 0)
                        {
                            output.Add(new ListItemBlock($"{n,2}. ", liRuns));
                            n++;
                        }
                    }
                    break;

                case "HR":
                    output.Add(new HorizontalRuleBlock());
                    break;

                // Inline-only at block level — wrap whole element as a paragraph if non-empty.
                case "A": case "STRONG": case "B": case "EM": case "I": case "CODE": case "SPAN":
                    var aRuns = CollectInline(el, RunStyle.Plain, ctx);
                    if (aRuns.Count > 0) output.Add(new ParagraphBlock(aRuns));
                    break;

                // Anything else (article, section, div, figure, header, footer, aside, nav,
                // main, table — we don't try to render tabular layout — etc.) — recurse so
                // we pick up the block-level descendants. SmartReader has already pruned the
                // chrome down to readable prose.
                default:
                    WalkBlocks(el, output, ctx);
                    break;
            }
        }
    }

    private static IEnumerable<IElement> DirectChildren(IElement parent, string tag)
    {
        foreach (var child in parent.ChildNodes)
        {
            if (child is IElement c && string.Equals(c.TagName, tag, StringComparison.Ordinal))
                yield return c;
        }
    }

    private static IReadOnlyList<Run> CollectInline(INode node, RunStyle inheritedStyle, ExtractCtx ctx)
    {
        var runs = new List<Run>();
        WalkInline(node, inheritedStyle, runs, ctx);
        return Coalesce(runs);
    }

    private static void WalkInline(INode node, RunStyle style, List<Run> output, ExtractCtx ctx)
    {
        foreach (var child in node.ChildNodes)
        {
            switch (child)
            {
                case IText t:
                    var text = NormalizeWhitespace(t.TextContent);
                    if (text.Length > 0) output.Add(new Run(text, style));
                    break;

                case IElement el:
                    switch (el.TagName)
                    {
                        case "BR":
                            output.Add(new Run("\n", style));
                            break;

                        case "STRONG": case "B":
                            WalkInline(el, style | RunStyle.Bold, output, ctx);
                            break;

                        case "EM": case "I":
                            // Italic is intentionally not surfaced — PuTTY doesn't render it
                            // and we don't want meaning-bearing emphasis to disappear silently.
                            // Render plain. (Bold-emphasized italics still get bold via parent.)
                            WalkInline(el, style, output, ctx);
                            break;

                        case "CODE":
                            WalkInline(el, style | RunStyle.Code, output, ctx);
                            break;

                        case "A":
                            EmitAnchor(el, style, output, ctx);
                            break;

                        case "IMG":
                            // Drop alt text inline as plain — better than silence.
                            var alt = el.GetAttribute("alt");
                            if (!string.IsNullOrWhiteSpace(alt))
                                output.Add(new Run($"[image: {alt.Trim()}]", style));
                            break;

                        case "SCRIPT": case "STYLE": case "NOSCRIPT":
                            break;

                        default:
                            WalkInline(el, style, output, ctx);
                            break;
                    }
                    break;
            }
        }
    }

    private static void EmitAnchor(IElement el, RunStyle inheritedStyle, List<Run> output, ExtractCtx ctx)
    {
        var href = el.GetAttribute("href");
        if (string.IsNullOrWhiteSpace(href)
            || !Uri.TryCreate(ctx.BaseUrl, href, out var abs)
            || (abs.Scheme != Uri.UriSchemeHttp && abs.Scheme != Uri.UriSchemeHttps))
        {
            // Not a usable link — render the inner text as plain.
            WalkInline(el, inheritedStyle, output, ctx);
            return;
        }

        var key = abs.ToString();
        if (!ctx.SeenLinkUrls.TryGetValue(key, out var index))
        {
            var rawText = (el.TextContent ?? string.Empty).Trim();
            if (rawText.Length > 80) rawText = rawText[..77] + "...";
            if (rawText.Length == 0) rawText = abs.Host;
            ctx.Links.Add(new ReaderLink(rawText, abs));
            index = ctx.Links.Count;
            ctx.SeenLinkUrls[key] = index;
        }

        // Inner inline content with Link bit set, then a [N] reference styled as Link too.
        var linkStyle = inheritedStyle | RunStyle.Link;
        var before = output.Count;
        WalkInline(el, linkStyle, output, ctx);
        if (output.Count == before)
        {
            // Anchor with no text content (e.g. wrapping an image we dropped) — render the host.
            output.Add(new Run(abs.Host, linkStyle, index));
        }
        else
        {
            // Stamp LinkIndex onto the runs we just emitted so they all carry it (used if we
            // later wire Enter-on-link to reopen as a new ReaderScreen).
            for (var i = before; i < output.Count; i++)
            {
                var r = output[i];
                output[i] = new Run(r.Text, r.Style, index);
            }
        }
        output.Add(new Run($" [{index}]", linkStyle, index));
    }

    private static string NormalizeWhitespace(string text)
    {
        if (string.IsNullOrEmpty(text)) return string.Empty;
        // Collapse runs of whitespace (including newlines from source HTML formatting) into
        // single spaces. Paragraph boundaries are encoded by block structure, not whitespace.
        var sb = new System.Text.StringBuilder(text.Length);
        var lastWasSpace = false;
        foreach (var ch in text)
        {
            if (char.IsWhiteSpace(ch))
            {
                if (!lastWasSpace) { sb.Append(' '); lastWasSpace = true; }
            }
            else
            {
                sb.Append(ch);
                lastWasSpace = false;
            }
        }
        return sb.ToString();
    }

    // Merge adjacent runs that share the same Style+LinkIndex — keeps the rendered line's run
    // list minimal so we don't issue an SGR change every other character.
    private static IReadOnlyList<Run> Coalesce(List<Run> runs)
    {
        if (runs.Count <= 1) return runs;
        var merged = new List<Run>(runs.Count);
        var current = runs[0];
        for (var i = 1; i < runs.Count; i++)
        {
            var next = runs[i];
            if (next.Style == current.Style && next.LinkIndex == current.LinkIndex)
            {
                current = current with { Text = current.Text + next.Text };
            }
            else
            {
                merged.Add(current);
                current = next;
            }
        }
        merged.Add(current);
        return merged;
    }
}
