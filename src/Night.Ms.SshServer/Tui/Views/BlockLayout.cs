using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Views;

// Pure layout pass: takes a block stream + a column width, produces a flat list of
// already-wrapped lines ready for painting. No Terminal.Gui dependency — easy to unit-test
// against fixed widths and easy to re-run when the viewport resizes.
//
// Wrapping is greedy whitespace: each Run is split on spaces (and on '\n' for explicit
// breaks from <br>); tokens are packed onto the current line until the next would
// overflow. Tokens longer than the column are hard-broken at the column boundary so a URL
// or huge identifier still renders something rather than disappearing off-screen.
//
// Inline images: ImageBlocks consult the supplied image-resolver delegate at layout time.
// If the resolver returns a CellGrid, the image renders as a sequence of ImageRowLines
// (one per cell row); if not (still fetching, or fetch failed), the layout reserves rows
// based on the intrinsic <img> aspect hint and renders a placeholder line so the body
// height stays roughly stable when the real image arrives.
internal static class BlockLayout
{
    internal sealed record RenderedRun(string Text, RunStyle Style, int LinkIndex = 0);

    internal abstract record RenderedLine;
    internal sealed record TextLine(IReadOnlyList<RenderedRun> Runs) : RenderedLine;
    internal sealed record ImageRowLine(IReadOnlyList<Cell> Cells) : RenderedLine;

    // Default placeholder height (in cell rows) used when an image hasn't been fetched yet
    // and intrinsic dimensions weren't supplied on the <img> element.
    private const int PlaceholderImageRows = 8;

    public static IReadOnlyList<RenderedLine> Layout(IReadOnlyList<ArticleBlock> blocks, int width) =>
        Layout(blocks, width, _ => null);

    public static IReadOnlyList<RenderedLine> Layout(
        IReadOnlyList<ArticleBlock> blocks,
        int width,
        Func<Uri, CellGrid?> imageResolver)
    {
        var output = new List<RenderedLine>();
        LayoutBlocks(blocks, Math.Max(width, 1), output, imageResolver);
        return output;
    }

    private static void LayoutBlocks(
        IReadOnlyList<ArticleBlock> blocks,
        int width,
        List<RenderedLine> output,
        Func<Uri, CellGrid?> imageResolver)
    {
        for (var i = 0; i < blocks.Count; i++)
        {
            if (i > 0) output.Add(EmptyLine);
            switch (blocks[i])
            {
                case HeadingBlock h:
                    LayoutWrapped(WithStyle(h.Runs, RunStyle.Heading), width, output);
                    break;
                case ParagraphBlock p:
                    LayoutWrapped(p.Runs, width, output);
                    break;
                case CodeBlock c:
                    LayoutCodeBlock(c, width, output);
                    break;
                case BlockquoteBlock bq:
                    LayoutBlockquote(bq, width, output, imageResolver);
                    break;
                case ListItemBlock li:
                    LayoutListItem(li, width, output);
                    break;
                case ImageBlock img:
                    LayoutImageBlock(img, width, output, imageResolver);
                    break;
                case TableBlock tb:
                    LayoutTable(tb, width, output);
                    break;
                case HorizontalRuleBlock:
                    output.Add(new TextLine(new[] { new RenderedRun(new string('─', width), RunStyle.Plain) }));
                    break;
            }
        }
    }

    // Lay out an ImageBlock either as real cell rows (resolver returned a CellGrid) or as a
    // text placeholder sized from the intrinsic <img> aspect hint. The placeholder height
    // exists so loading-then-arriving images don't reflow the entire body downward.
    private static void LayoutImageBlock(
        ImageBlock img,
        int width,
        List<RenderedLine> output,
        Func<Uri, CellGrid?> imageResolver)
    {
        var resolved = imageResolver(img.Source);
        if (resolved is not null && resolved.Width > 0 && resolved.Height > 0)
        {
            for (var y = 0; y < resolved.Height; y++)
            {
                var row = new Cell[Math.Min(resolved.Width, width)];
                for (var x = 0; x < row.Length; x++) row[x] = resolved[x, y];
                output.Add(new ImageRowLine(row));
            }
            return;
        }

        var placeholderRows = EstimatePlaceholderRows(img, width);
        var label = img.Alt is { Length: > 0 } a ? $"[image: {a}]" : "[image]";
        if (label.Length > width) label = label[..width];
        output.Add(new TextLine(new[] { new RenderedRun(label, RunStyle.Plain) }));
        for (var i = 1; i < placeholderRows; i++) output.Add(EmptyLine);
    }

    // w3m-style two-pass table layout. Per-column min width = max longest unbreakable
    // token across that column's cells; per-column max width = max natural single-line
    // width across that column's cells. If the column maxes fit in the available width,
    // use them; otherwise distribute the slack between min and max proportionally to
    // each column's stretch potential. Header cells (<th>) render bold; light vertical
    // separators sit between columns. Colspan/rowspan are not honored — every cell is
    // 1×1 and short rows leave their tail columns blank.
    private static void LayoutTable(TableBlock t, int width, List<RenderedLine> output)
    {
        var rows = t.Rows;
        if (rows.Count == 0) return;
        var cols = 0;
        foreach (var r in rows) if (r.Cells.Count > cols) cols = r.Cells.Count;
        if (cols == 0) return;

        // Light vertical separator " │ " between columns; no outer border.
        const string SeparatorText = " │ ";
        var sepWidth = SeparatorText.Length;
        var totalSepsWidth = sepWidth * (cols - 1);

        // Measure per-column min/max.
        var min = new int[cols];
        var max = new int[cols];
        foreach (var row in rows)
        {
            for (var c = 0; c < row.Cells.Count; c++)
            {
                var (cellMin, cellMax) = MeasureCellWidth(row.Cells[c].Runs);
                if (cellMin > min[c]) min[c] = cellMin;
                if (cellMax > max[c]) max[c] = cellMax;
            }
        }
        for (var c = 0; c < cols; c++)
        {
            if (min[c] == 0) min[c] = 1;
            if (max[c] < min[c]) max[c] = min[c];
        }

        var widths = AllocateColumnWidths(min, max, Math.Max(width - totalSepsWidth, cols));

        // Render row by row.
        var sepRun = new RenderedRun(SeparatorText, RunStyle.Quote);
        foreach (var row in rows)
        {
            // Wrap each cell to its column's allocated width.
            var perCellLines = new List<IReadOnlyList<RenderedRun>>[cols];
            for (var c = 0; c < cols; c++)
            {
                if (c >= row.Cells.Count)
                {
                    perCellLines[c] = new List<IReadOnlyList<RenderedRun>>();
                    continue;
                }
                var cell = row.Cells[c];
                var runs = cell.IsHeader ? WithStyle(cell.Runs, RunStyle.Bold) : cell.Runs;
                var cellOut = new List<RenderedLine>();
                LayoutWrapped(runs, widths[c], cellOut);
                perCellLines[c] = cellOut.OfType<TextLine>().Select(l => (IReadOnlyList<RenderedRun>)l.Runs).ToList();
            }

            var rowHeight = 1;
            for (var c = 0; c < cols; c++)
                if (perCellLines[c].Count > rowHeight) rowHeight = perCellLines[c].Count;

            for (var lineIdx = 0; lineIdx < rowHeight; lineIdx++)
            {
                var rowRuns = new List<RenderedRun>();
                for (var c = 0; c < cols; c++)
                {
                    if (c > 0) rowRuns.Add(sepRun);
                    var cellLine = lineIdx < perCellLines[c].Count
                        ? perCellLines[c][lineIdx]
                        : Array.Empty<RenderedRun>();
                    var used = AddCellRunsAndPad(rowRuns, cellLine, widths[c]);
                    if (used < widths[c])
                        rowRuns.Add(new RenderedRun(new string(' ', widths[c] - used), RunStyle.Plain));
                }
                output.Add(new TextLine(MergeAdjacent(rowRuns)));
            }
        }
    }

    // Append a pre-wrapped cell line into the row's run list, returning the visible width
    // consumed (sum of run text lengths, capped at colWidth). Used by LayoutTable to know
    // how much trailing space-padding to add to align the next column's separator.
    private static int AddCellRunsAndPad(List<RenderedRun> output, IReadOnlyList<RenderedRun> cellLine, int colWidth)
    {
        var used = 0;
        foreach (var run in cellLine)
        {
            if (used >= colWidth) break;
            if (used + run.Text.Length <= colWidth)
            {
                output.Add(run);
                used += run.Text.Length;
            }
            else
            {
                var slice = run.Text[..(colWidth - used)];
                output.Add(new RenderedRun(slice, run.Style, run.LinkIndex));
                used = colWidth;
            }
        }
        return used;
    }

    // Compute (min, max) width that a cell wants:
    //   min = longest unbreakable token (no whitespace inside); the column can't go narrower
    //         without truncating that token mid-word.
    //   max = longest natural single-line width across explicit \n-separated lines; the
    //         column doesn't get value from going wider than this.
    private static (int min, int max) MeasureCellWidth(IReadOnlyList<Run> runs)
    {
        var concat = new System.Text.StringBuilder();
        foreach (var r in runs) concat.Append(r.Text);
        var min = 0;
        var max = 0;
        foreach (var line in concat.ToString().Split('\n'))
        {
            var lineLen = 0;
            foreach (var token in line.Split(' ', StringSplitOptions.RemoveEmptyEntries))
            {
                if (token.Length > min) min = token.Length;
                if (lineLen > 0) lineLen++;
                lineLen += token.Length;
            }
            if (lineLen > max) max = lineLen;
        }
        return (Math.Max(min, 1), Math.Max(max, 1));
    }

    // Distribute width across columns. If max widths fit, use them and spread the remainder
    // for visual balance; if even the mins overflow, accept clipping; otherwise allocate
    // min + a proportional share of remaining slack.
    // otherwise allocate min + proportional share of remaining slack.
    private static int[] AllocateColumnWidths(int[] min, int[] max, int avail)
    {
        var cols = min.Length;
        var widths = new int[cols];
        var totalMax = 0;
        var totalMin = 0;
        for (var c = 0; c < cols; c++)
        {
            totalMax += max[c];
            totalMin += min[c];
        }

        if (totalMax <= avail)
        {
            // Roomy table — each column gets its natural max width and the remainder spreads
            // evenly so the table consumes the available width (a left-flush narrow table
            // surrounded by void looks unloved next to centered prose).
            Array.Copy(max, widths, cols);
            var remainder = avail - totalMax;
            for (var c = 0; remainder > 0 && cols > 0; c = (c + 1) % cols)
            {
                widths[c]++;
                remainder--;
            }
            return widths;
        }

        if (totalMin >= avail)
        {
            // Even the minimum widths overflow — give each column its min and accept that
            // the table is wider than the body column. Painting will just clip.
            Array.Copy(min, widths, cols);
            return widths;
        }

        // Mid case: start at min, distribute the surplus (avail - totalMin) in proportion to
        // each column's stretch potential (max - min). Round down; sweep any remainder
        // round-robin starting from the column with the largest unmet desire.
        var slack = avail - totalMin;
        var totalStretch = 0;
        for (var c = 0; c < cols; c++) totalStretch += max[c] - min[c];

        if (totalStretch == 0)
        {
            Array.Copy(min, widths, cols);
            return widths;
        }

        var allocated = 0;
        for (var c = 0; c < cols; c++)
        {
            var stretch = max[c] - min[c];
            var add = (int)((long)slack * stretch / totalStretch);
            widths[c] = min[c] + add;
            allocated += add;
        }
        var leftover = slack - allocated;
        for (var c = 0; leftover > 0 && cols > 0; c = (c + 1) % cols)
        {
            if (widths[c] < max[c])
            {
                widths[c]++;
                leftover--;
            }
            else if (cols == 1)
            {
                widths[c]++;
                leftover--;
            }
        }
        return widths;
    }

    // Reserve cell rows for an image still being fetched. Use the intrinsic px hint when
    // present (mapping ~8 source pixels per cell, with the half-block 2x vertical doubling
    // already factored), otherwise fall back to PlaceholderImageRows so the layout doesn't
    // underestimate by a wide margin.
    private static int EstimatePlaceholderRows(ImageBlock img, int width)
    {
        if (img.Width is { } iw && img.Height is { } ih && iw > 0)
        {
            var cellRows = (int)Math.Ceiling(ih * width / (double)(iw * 2));
            if (cellRows > 0) return Math.Min(cellRows, 40);
        }
        return PlaceholderImageRows;
    }

    private static IReadOnlyList<Run> WithStyle(IReadOnlyList<Run> runs, RunStyle add)
    {
        var copy = new Run[runs.Count];
        for (var i = 0; i < runs.Count; i++)
        {
            var r = runs[i];
            copy[i] = new Run(r.Text, r.Style | add, r.LinkIndex);
        }
        return copy;
    }

    private static void LayoutCodeBlock(CodeBlock c, int width, List<RenderedLine> output)
    {
        // Preserve whitespace; truncate over-wide lines so we don't tear the layout. Code
        // blocks are deliberately left-anchored (no centering) — alignment matters.
        foreach (var line in c.Text.Replace("\t", "    ").Split('\n'))
        {
            var truncated = line.Length > width ? line[..width] : line;
            output.Add(new TextLine(new[] { new RenderedRun(truncated, RunStyle.Code) }));
        }
    }

    private static void LayoutBlockquote(
        BlockquoteBlock bq,
        int width,
        List<RenderedLine> output,
        Func<Uri, CellGrid?> imageResolver)
    {
        var innerWidth = Math.Max(width - 2, 1);
        var innerLines = new List<RenderedLine>();
        LayoutBlocks(bq.Children, innerWidth, innerLines, imageResolver);
        foreach (var line in innerLines)
        {
            // Image rows inside a blockquote: leave the gutter as a Quote-styled prefix and
            // pass the cells through unchanged. Won't happen often in real articles but it
            // shouldn't crash if SmartReader hands us <blockquote><img/></blockquote>.
            if (line is ImageRowLine imgRow)
            {
                output.Add(new TextLine(new[] { new RenderedRun("│ ", RunStyle.Quote) }));
                output.Add(imgRow);
                continue;
            }
            var textLine = (TextLine)line;
            var newRuns = new List<RenderedRun>(textLine.Runs.Count + 1)
            {
                new("│ ", RunStyle.Quote),
            };
            foreach (var r in textLine.Runs)
            {
                newRuns.Add(new RenderedRun(r.Text, r.Style | RunStyle.Quote, r.LinkIndex));
            }
            output.Add(new TextLine(newRuns));
        }
    }

    private static void LayoutListItem(ListItemBlock li, int width, List<RenderedLine> output)
    {
        var marker = li.Marker;
        var indent = marker.Length;
        var contentWidth = Math.Max(width - indent, 1);
        var itemLines = new List<RenderedLine>();
        LayoutWrapped(li.Runs, contentWidth, itemLines);
        for (var i = 0; i < itemLines.Count; i++)
        {
            // List items hold inline runs only — the wrapped output is always TextLine.
            var textLine = (TextLine)itemLines[i];
            var prefix = i == 0 ? marker : new string(' ', indent);
            var newRuns = new List<RenderedRun>(textLine.Runs.Count + 1)
            {
                new(prefix, RunStyle.Plain),
            };
            newRuns.AddRange(textLine.Runs);
            output.Add(new TextLine(newRuns));
        }
    }

    // Greedy whitespace word-wrap. Splits each run on spaces (and on \n for explicit breaks
    // from <br>), then packs tokens onto the current line until the next would overflow.
    // Tokens longer than the column are hard-broken at the column boundary.
    //
    // The separator space between two words inherits the *previous* word's style and link
    // index, so multi-word links ("Click here [3]") render with a continuous underline
    // through the spaces rather than blinking on/off per word.
    private static void LayoutWrapped(IReadOnlyList<Run> runs, int width, List<RenderedLine> output)
    {
        var line = new List<RenderedRun>();
        var lineLen = 0;
        var firstWord = true;
        var lastStyle = RunStyle.Plain;
        var lastLink = 0;

        void Flush()
        {
            output.Add(new TextLine(MergeAdjacent(line)));
            line = new List<RenderedRun>();
            lineLen = 0;
            firstWord = true;
        }

        void AddWord(string word, RunStyle style, int link)
        {
            if (word.Length == 0) return;

            // Hard-break tokens longer than the column.
            while (word.Length > width)
            {
                if (!firstWord) Flush();
                line.Add(new RenderedRun(word[..width], style, link));
                output.Add(new TextLine(MergeAdjacent(line)));
                line = new List<RenderedRun>();
                lineLen = 0;
                firstWord = true;
                word = word[width..];
            }

            var needed = (firstWord ? 0 : 1) + word.Length;
            if (lineLen + needed > width && !firstWord)
            {
                Flush();
                needed = word.Length;
            }
            if (!firstWord)
            {
                line.Add(new RenderedRun(" ", lastStyle, lastLink));
                lineLen += 1;
            }
            line.Add(new RenderedRun(word, style, link));
            lineLen += word.Length;
            firstWord = false;
            lastStyle = style;
            lastLink = link;
        }

        foreach (var run in runs)
        {
            if (run.Text.IndexOf('\n') >= 0)
            {
                var parts = run.Text.Split('\n');
                for (var i = 0; i < parts.Length; i++)
                {
                    if (i > 0) Flush();
                    foreach (var word in parts[i].Split(' ', StringSplitOptions.RemoveEmptyEntries))
                        AddWord(word, run.Style, run.LinkIndex);
                }
            }
            else
            {
                foreach (var word in run.Text.Split(' ', StringSplitOptions.RemoveEmptyEntries))
                    AddWord(word, run.Style, run.LinkIndex);
            }
        }

        if (line.Count > 0) Flush();
    }

    private static IReadOnlyList<RenderedRun> MergeAdjacent(List<RenderedRun> runs)
    {
        if (runs.Count <= 1) return runs;
        var merged = new List<RenderedRun>(runs.Count);
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

    private static readonly RenderedLine EmptyLine = new TextLine(Array.Empty<RenderedRun>());
}
