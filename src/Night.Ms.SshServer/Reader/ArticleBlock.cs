namespace Night.Ms.SshServer.Reader;

// Inline run style flags. Plain == body text; the others compose. Heading wins over the
// rest (whole heading line renders as bright-yellow bold even if a child <strong> sets
// Bold). Quote isn't a run flag — it's encoded by wrapping blocks in BlockquoteBlock so
// the renderer can prefix every nested line with the gutter.
[Flags]
public enum RunStyle
{
    Plain = 0,
    Bold = 1 << 0,
    Code = 1 << 1,
    Link = 1 << 2,
    Heading = 1 << 3,
    Quote = 1 << 4,
}

// One styled span of text inside a paragraph, heading, or list item. LinkIndex is 1-based
// and points into ReaderArticle.Links; 0 means "not a link." The renderer uses it to
// append a [N] reference next to link text so the reader can match it to the links pane.
public sealed record Run(string Text, RunStyle Style = RunStyle.Plain, int LinkIndex = 0);

// Discriminated union of block-level article elements. Kept deliberately small: this is a
// reader, not a fidelity-preserving HTML renderer.
public abstract record ArticleBlock;

public sealed record HeadingBlock(int Level, IReadOnlyList<Run> Runs) : ArticleBlock;
public sealed record ParagraphBlock(IReadOnlyList<Run> Runs) : ArticleBlock;
public sealed record CodeBlock(string Text) : ArticleBlock;
public sealed record BlockquoteBlock(IReadOnlyList<ArticleBlock> Children) : ArticleBlock;
public sealed record ListItemBlock(string Marker, IReadOnlyList<Run> Runs) : ArticleBlock;
public sealed record HorizontalRuleBlock : ArticleBlock;
