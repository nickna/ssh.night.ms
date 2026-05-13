using AngleSharp.Html.Parser;
using Night.Ms.SshServer.Reader;

namespace Night.Ms.SshServer.Tests;

public class HtmlBlockExtractorImageLiftTests
{
    private static readonly Uri BaseUrl = new("https://en.wikipedia.org/wiki/Tests_(album)");

    [Fact]
    public void Anchor_wrapped_image_lifts_to_image_block()
    {
        // The canonical Wikipedia / CMS pattern: <a href="..."><img/></a>.
        var html = """
            <html><body>
              <a href="/wiki/File:cover.jpg"><img src="//upload.wikimedia.org/foo.jpg" alt="cover"/></a>
            </body></html>
            """;
        var blocks = ExtractBlocks(html);

        Assert.Single(blocks.OfType<ImageBlock>());
        var img = blocks.OfType<ImageBlock>().Single();
        Assert.Equal("https://upload.wikimedia.org/foo.jpg", img.Source.ToString());
        Assert.Equal("cover", img.Alt);
    }

    [Fact]
    public void Span_wrapping_anchor_wrapping_image_still_lifts()
    {
        // Wikipedia's actual pattern wraps the anchor in a span:
        //   <span typeof="mw:File/Frameless"><a><img/></a></span>
        var html = """
            <html><body>
              <span typeof="mw:File/Frameless">
                <a href="/wiki/File:cover.jpg" class="mw-file-description">
                  <img src="//upload.wikimedia.org/cover.jpg" alt="cover" width="250" height="250"/>
                </a>
              </span>
            </body></html>
            """;
        var blocks = ExtractBlocks(html);

        var img = Assert.Single(blocks.OfType<ImageBlock>());
        Assert.Equal("https://upload.wikimedia.org/cover.jpg", img.Source.ToString());
        Assert.Equal(250, img.Width);
        Assert.Equal(250, img.Height);
    }

    [Fact]
    public void Image_inside_infobox_table_lifts_above_the_table_and_image_only_cell_is_skipped()
    {
        // Trimmed Wikipedia infobox pattern: title row, image cell row, metadata row.
        // Expected output: image lifted above the table; image-only cell does not show
        // as "[image: alt]" placeholder text inside the table.
        var html = """
            <html><body>
              <table class="infobox">
                <tbody>
                  <tr><th colspan="2">Tests</th></tr>
                  <tr><td colspan="2">
                    <span typeof="mw:File/Frameless">
                      <a href="/wiki/File:cover.jpg" class="mw-file-description">
                        <img src="//upload.wikimedia.org/cover.jpg" alt="album cover" width="250" height="250"/>
                      </a>
                    </span>
                  </td></tr>
                  <tr><th>Released</th><td>1998</td></tr>
                </tbody>
              </table>
            </body></html>
            """;
        var blocks = ExtractBlocks(html);

        // Image lifted out as a peer block.
        var img = Assert.Single(blocks.OfType<ImageBlock>());
        Assert.Equal("https://upload.wikimedia.org/cover.jpg", img.Source.ToString());

        // Table still emitted, with image-only cell skipped.
        var table = Assert.Single(blocks.OfType<TableBlock>());
        Assert.All(table.Rows, row =>
            Assert.DoesNotContain(row.Cells, cell =>
                cell.Runs.Any(r => r.Text.Contains("[image:"))));
    }

    [Fact]
    public void Paragraph_with_only_image_lifts_to_image_block()
    {
        // The blog hero pattern: <p><img/></p>.
        var html = """
            <html><body>
              <p><img src="https://example.com/hero.png" alt="hero"/></p>
            </body></html>
            """;
        var blocks = ExtractBlocks(html);

        Assert.Single(blocks.OfType<ImageBlock>());
        Assert.DoesNotContain(blocks.OfType<ParagraphBlock>(), p => p.Runs.Any(r => r.Text.Contains("[image:")));
    }

    [Fact]
    public void Paragraph_mixing_text_and_image_splits_into_three_blocks()
    {
        var html = """
            <html><body>
              <p>before <img src="https://example.com/inline.png" alt="x"/> after</p>
            </body></html>
            """;
        var blocks = ExtractBlocks(html);

        var img = Assert.Single(blocks.OfType<ImageBlock>());
        Assert.Equal("https://example.com/inline.png", img.Source.ToString());

        var paragraphs = blocks.OfType<ParagraphBlock>().ToList();
        Assert.Equal(2, paragraphs.Count);
        Assert.Contains(paragraphs, p => p.Runs.Any(r => r.Text.Contains("before")));
        Assert.Contains(paragraphs, p => p.Runs.Any(r => r.Text.Contains("after")));
    }

    private static IReadOnlyList<ArticleBlock> ExtractBlocks(string html)
    {
        var doc = new HtmlParser().ParseDocument(html);
        var (blocks, _) = HtmlBlockExtractor.Extract(doc, BaseUrl);
        return blocks;
    }
}
