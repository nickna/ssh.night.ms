using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Tui.Views;

namespace Night.Ms.SshServer.Tests;

public class BlockLayoutTableTests
{
    [Fact]
    public void Two_column_table_with_short_cells_fits_on_one_row_per_table_row()
    {
        var table = new TableBlock(new[]
        {
            new TableRow(new[]
            {
                Cell("Alice", header: false),
                Cell("Engineer", header: false),
            }),
            new TableRow(new[]
            {
                Cell("Bob", header: false),
                Cell("Designer", header: false),
            }),
        });

        var lines = BlockLayout.Layout(new ArticleBlock[] { table }, width: 40);

        // Two rows of content, no per-row wrapping needed.
        var textLines = lines.OfType<BlockLayout.TextLine>().ToList();
        Assert.Equal(2, textLines.Count);

        var firstRowText = string.Concat(textLines[0].Runs.Select(r => r.Text));
        Assert.Contains("Alice", firstRowText);
        Assert.Contains("Engineer", firstRowText);
        Assert.Contains("│", firstRowText);
    }

    [Fact]
    public void Header_row_runs_carry_bold_style()
    {
        var table = new TableBlock(new[]
        {
            new TableRow(new[]
            {
                Cell("Name", header: true),
                Cell("Role", header: true),
            }),
            new TableRow(new[]
            {
                Cell("Alice", header: false),
                Cell("Eng",   header: false),
            }),
        });

        var lines = BlockLayout.Layout(new ArticleBlock[] { table }, width: 40);
        var headerLine = (BlockLayout.TextLine)lines[0];

        // The "Name" run should carry RunStyle.Bold; the trailing-pad and separator runs
        // shouldn't (they belong to the structure, not the content).
        var nameRun = headerLine.Runs.First(r => r.Text.Contains("Name"));
        Assert.True(nameRun.Style.HasFlag(RunStyle.Bold));

        var bodyLine = (BlockLayout.TextLine)lines[1];
        var aliceRun = bodyLine.Runs.First(r => r.Text.Contains("Alice"));
        Assert.False(aliceRun.Style.HasFlag(RunStyle.Bold));
    }

    [Fact]
    public void Long_cell_text_wraps_within_its_column_and_extends_row_height()
    {
        // A short column and a long-text column sharing 30 cells. The long cell should be
        // packed into multiple lines while the short cell sits on the first row only.
        var table = new TableBlock(new[]
        {
            new TableRow(new[]
            {
                Cell("ID", header: true),
                Cell("Description", header: true),
            }),
            new TableRow(new[]
            {
                Cell("1", header: false),
                Cell("This is a fairly long description that should wrap across multiple cell lines", header: false),
            }),
        });

        var lines = BlockLayout.Layout(new ArticleBlock[] { table }, width: 30);
        var textLines = lines.OfType<BlockLayout.TextLine>().ToList();

        // Header (1 line) + body row spanning multiple lines.
        Assert.True(textLines.Count >= 3, $"Expected ≥3 lines (header + multi-line body), got {textLines.Count}");
    }

    [Fact]
    public void Width_below_column_minimums_clips_rather_than_crashes()
    {
        // 6-cell viewport, 3 columns of 5-char single-token cells: the column mins exceed
        // the available width. Layout should still produce output (using min widths, table
        // overflows the viewport — RichArticleView's per-line clip handles the rest).
        var table = new TableBlock(new[]
        {
            new TableRow(new[]
            {
                Cell("AAAAA", header: false),
                Cell("BBBBB", header: false),
                Cell("CCCCC", header: false),
            }),
        });

        var lines = BlockLayout.Layout(new ArticleBlock[] { table }, width: 6);

        // Doesn't crash, produces at least one line.
        var textLines = lines.OfType<BlockLayout.TextLine>().ToList();
        Assert.NotEmpty(textLines);
    }

    private static TableCell Cell(string text, bool header) =>
        new(new[] { new Run(text, RunStyle.Plain) }, header);
}
