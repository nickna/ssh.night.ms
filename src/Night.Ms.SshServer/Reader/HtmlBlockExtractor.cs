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
                    EmitBlocksLiftingImages(el, output, ctx);
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

                case "IMG":
                    var img = TryBuildImageBlock(el, ctx);
                    if (img is not null) output.Add(img);
                    break;

                case "TABLE":
                    // Wikipedia infoboxes (and many CMS templates) use <table> as a layout
                    // container with an image cell + metadata cells. Lift any images out as
                    // peer ImageBlocks first — they're the table's real content — then build
                    // the metadata table from what's left. Without this lift, infobox images
                    // get stranded as "[image: alt]" placeholder text inside cells.
                    ExtractImagesFromDescendants(el, output, ctx);
                    var table = BuildTableBlock(el, ctx);
                    if (table is not null) output.Add(table);
                    break;

                // Wrapper elements that almost-always carry inline content but occasionally
                // wrap a buried image (Wikipedia's `<span><a><img/></a></span>` pattern, or
                // bare `<a><img/></a>` blog images). When an image is buried inside, lift it
                // out via the recursive splitter instead of letting the inline path collapse
                // it to "[image: alt]" placeholder text.
                case "A" or "STRONG" or "B" or "EM" or "I" or "CODE" or "SPAN" when ContainsImg(el):
                    EmitBlocksLiftingImages(el, output, ctx);
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

    // Walk `parent`'s children and emit a sequence of blocks: text-only spans become
    // ParagraphBlocks, IMG descendants become standalone ImageBlocks (recursing through
    // wrapper elements like <a>, <span>, <picture>, <figure> to find them), and everything
    // else stays inline within the surrounding paragraph. Used for <p> (the canonical blog
    // hero image pattern `<p><img/></p>` works because the IMG splits the paragraph) and
    // for inline wrappers like <a><img/></a> where the image needs to be lifted out of the
    // anchor's inline content. Recursion handles deeply-nested wrappers like Wikipedia's
    // `<span><a><img/></a></span>` chain.
    private static void EmitBlocksLiftingImages(IElement parent, List<ArticleBlock> output, ExtractCtx ctx)
    {
        var pendingRuns = new List<Run>();

        void FlushRuns()
        {
            if (pendingRuns.Count == 0) return;
            var coalesced = Coalesce(pendingRuns);
            if (coalesced.Count > 0) output.Add(new ParagraphBlock(coalesced));
            pendingRuns = new List<Run>();
        }

        foreach (var child in parent.ChildNodes)
        {
            switch (child)
            {
                case IText t:
                    var text = NormalizeWhitespace(t.TextContent);
                    if (text.Length > 0) pendingRuns.Add(new Run(text, RunStyle.Plain));
                    break;

                case IElement el when string.Equals(el.TagName, "IMG", StringComparison.Ordinal):
                    FlushRuns();
                    var img = TryBuildImageBlock(el, ctx);
                    if (img is not null) output.Add(img);
                    break;

                case IElement el when ContainsImg(el):
                    // A wrapper element with a buried IMG — recurse to lift it. The image's
                    // wrapper (anchor, span, etc.) is discarded; surrounding text content of
                    // the wrapper is collected as a paragraph alongside the image.
                    FlushRuns();
                    EmitBlocksLiftingImages(el, output, ctx);
                    break;

                case IElement el:
                    // No image in this subtree — collect inline runs from it (preserves
                    // bold/italic/code/link style cascade via WalkInlineSingleElement).
                    WalkInlineSingleElement(el, RunStyle.Plain, pendingRuns, ctx);
                    break;
            }
        }
        FlushRuns();
    }

    // Recursively walk `parent` and emit an ImageBlock for every <img> descendant that has
    // a usable absolute http(s) src. Used by the TABLE case to lift infobox/layout-table
    // images out as peer blocks before the table itself is extracted. Order is DOM order.
    private static void ExtractImagesFromDescendants(INode parent, List<ArticleBlock> output, ExtractCtx ctx)
    {
        foreach (var child in parent.ChildNodes)
        {
            if (child is not IElement el) continue;
            if (string.Equals(el.TagName, "IMG", StringComparison.Ordinal))
            {
                var img = TryBuildImageBlock(el, ctx);
                if (img is not null) output.Add(img);
            }
            else
            {
                ExtractImagesFromDescendants(el, output, ctx);
            }
        }
    }

    // Walk a <table> into a TableBlock. Rows live directly under <table> or under any of
    // <thead>, <tbody>, <tfoot> — flatten by recursion. Each <tr>'s direct <td>/<th>
    // children become TableCells; cell contents are inline-only for now (nested blocks
    // collapse to flat runs). Returns null if the table is structurally empty so we don't
    // emit "[]" placeholders into the layout.
    private static TableBlock? BuildTableBlock(IElement table, ExtractCtx ctx)
    {
        var rows = new List<TableRow>();
        CollectRows(table, rows, ctx);
        if (rows.Count == 0) return null;
        return new TableBlock(rows);
    }

    private static void CollectRows(IElement node, List<TableRow> rows, ExtractCtx ctx)
    {
        foreach (var child in node.ChildNodes)
        {
            if (child is not IElement el) continue;
            switch (el.TagName)
            {
                case "TR":
                    var cells = new List<TableCell>();
                    foreach (var cellNode in el.ChildNodes)
                    {
                        if (cellNode is not IElement c) continue;
                        if (c.TagName != "TD" && c.TagName != "TH") continue;
                        // Image-only cells: the TABLE case already lifted these images to
                        // peer ImageBlocks above the table. Skip the cell so the table
                        // doesn't show "[image: alt]" placeholder text where the image was.
                        if (IsImageOnlyCell(c)) continue;
                        var runs = CollectInline(c, RunStyle.Plain, ctx);
                        cells.Add(new TableCell(runs, IsHeader: c.TagName == "TH"));
                    }
                    if (cells.Count > 0) rows.Add(new TableRow(cells));
                    break;

                case "THEAD": case "TBODY": case "TFOOT":
                    CollectRows(el, rows, ctx);
                    break;
            }
        }
    }

    // Build an ImageBlock from an <img> element if it has a usable absolute http(s) src.
    // Captures intrinsic width/height as integer hints when present and parseable; otherwise
    // null and the layout pass picks dimensions from the source aspect ratio at fetch time.
    private static ImageBlock? TryBuildImageBlock(IElement el, ExtractCtx ctx)
    {
        var rawSrc = el.GetAttribute("src");
        if (string.IsNullOrWhiteSpace(rawSrc)) return null;
        if (!Uri.TryCreate(ctx.BaseUrl, rawSrc, out var abs)) return null;
        if (abs.Scheme != Uri.UriSchemeHttp && abs.Scheme != Uri.UriSchemeHttps) return null;

        var alt = el.GetAttribute("alt");
        if (string.IsNullOrWhiteSpace(alt)) alt = null;
        else alt = alt.Trim();

        return new ImageBlock(abs, alt, ParsePxAttribute(el, "width"), ParsePxAttribute(el, "height"));
    }

    private static int? ParsePxAttribute(IElement el, string name)
    {
        var raw = el.GetAttribute(name);
        if (string.IsNullOrWhiteSpace(raw)) return null;
        // Strip "px" suffix, ignore percentages / em / etc. — only intrinsic pixel hints help us.
        var s = raw.Trim();
        if (s.EndsWith("px", StringComparison.OrdinalIgnoreCase)) s = s[..^2];
        return int.TryParse(s, out var n) && n > 0 ? n : null;
    }

    // Recursively look for an IMG anywhere under `parent`. Used by the WalkBlocks A-case
    // to decide whether to lift an anchor's image content out instead of inlining it.
    private static bool ContainsImg(INode parent)
    {
        foreach (var child in parent.ChildNodes)
        {
            if (child is IElement el)
            {
                if (string.Equals(el.TagName, "IMG", StringComparison.Ordinal)) return true;
                if (ContainsImg(el)) return true;
            }
        }
        return false;
    }

    // A cell that only carries an image (Wikipedia infobox image cell pattern): the
    // recursive text content is all whitespace, and there's at least one <img> descendant.
    // We use this to suppress empty placeholder cells in the table after the image has
    // already been lifted to a peer ImageBlock above the table.
    private static bool IsImageOnlyCell(IElement cell)
    {
        if (!string.IsNullOrWhiteSpace(cell.TextContent)) return false;
        return ContainsImg(cell);
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
                    WalkInlineSingleElement(el, style, output, ctx);
                    break;
            }
        }
    }

    // Per-element inline dispatcher — split out from WalkInline so that callers walking a
    // parent's children one-by-one (e.g. the paragraph splitter that promotes <img> to its
    // own block) can reuse the same per-tag style cascade without re-implementing the switch.
    private static void WalkInlineSingleElement(IElement el, RunStyle style, List<Run> output, ExtractCtx ctx)
    {
        switch (el.TagName)
        {
            case "BR":
                output.Add(new Run("\n", style));
                break;

            case "STRONG": case "B":
                WalkInline(el, style | RunStyle.Bold, output, ctx);
                break;

            case "EM": case "I":
                WalkInline(el, style | RunStyle.Italic, output, ctx);
                break;

            case "CODE":
                WalkInline(el, style | RunStyle.Code, output, ctx);
                break;

            case "A":
                EmitAnchor(el, style, output, ctx);
                break;

            case "IMG":
                // Inline image inside a styled run (e.g. inside an <a> wrapping an icon):
                // the paragraph splitter handles the common case of P > IMG by lifting it to
                // a block. Here, just drop the alt as plain text — better than silence — so
                // mid-prose icons don't disappear and a wrapping <a> still keeps its anchor.
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
