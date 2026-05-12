using Night.Ms.SshServer.Reader;

namespace Night.Ms.SshServer.Tui.Views;

// Pure layout pass: takes a block stream + a column width, produces a flat list of
// already-wrapped lines ready for painting. No Terminal.Gui dependency — easy to unit-test
// against fixed widths and easy to re-run when the viewport resizes.
//
// Wrapping is greedy whitespace: each Run is split on spaces (and on '\n' for explicit
// breaks from <br>); tokens are packed onto the current line until the next would
// overflow. Tokens longer than the column are hard-broken at the column boundary so a URL
// or huge identifier still renders something rather than disappearing off-screen.
internal static class BlockLayout
{
    internal sealed record RenderedRun(string Text, RunStyle Style, int LinkIndex = 0);
    internal sealed record RenderedLine(IReadOnlyList<RenderedRun> Runs);

    public static IReadOnlyList<RenderedLine> Layout(IReadOnlyList<ArticleBlock> blocks, int width)
    {
        var output = new List<RenderedLine>();
        LayoutBlocks(blocks, Math.Max(width, 1), output);
        return output;
    }

    private static void LayoutBlocks(IReadOnlyList<ArticleBlock> blocks, int width, List<RenderedLine> output)
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
                    LayoutBlockquote(bq, width, output);
                    break;
                case ListItemBlock li:
                    LayoutListItem(li, width, output);
                    break;
                case HorizontalRuleBlock:
                    output.Add(new RenderedLine(new[] { new RenderedRun(new string('─', width), RunStyle.Plain) }));
                    break;
            }
        }
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
            output.Add(new RenderedLine(new[] { new RenderedRun(truncated, RunStyle.Code) }));
        }
    }

    private static void LayoutBlockquote(BlockquoteBlock bq, int width, List<RenderedLine> output)
    {
        var innerWidth = Math.Max(width - 2, 1);
        var innerLines = new List<RenderedLine>();
        LayoutBlocks(bq.Children, innerWidth, innerLines);
        foreach (var line in innerLines)
        {
            var newRuns = new List<RenderedRun>(line.Runs.Count + 1)
            {
                new("│ ", RunStyle.Quote),
            };
            foreach (var r in line.Runs)
            {
                newRuns.Add(new RenderedRun(r.Text, r.Style | RunStyle.Quote, r.LinkIndex));
            }
            output.Add(new RenderedLine(newRuns));
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
            var prefix = i == 0 ? marker : new string(' ', indent);
            var newRuns = new List<RenderedRun>(itemLines[i].Runs.Count + 1)
            {
                new(prefix, RunStyle.Plain),
            };
            newRuns.AddRange(itemLines[i].Runs);
            output.Add(new RenderedLine(newRuns));
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
            output.Add(new RenderedLine(MergeAdjacent(line)));
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
                output.Add(new RenderedLine(MergeAdjacent(line)));
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

    private static readonly RenderedLine EmptyLine = new(Array.Empty<RenderedRun>());
}
