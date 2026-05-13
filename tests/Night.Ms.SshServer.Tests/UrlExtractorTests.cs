using Night.Ms.SshServer.Tui.Chat;

namespace Night.Ms.SshServer.Tests;

public class UrlExtractorTests
{
    [Fact]
    public void Finds_single_png_url()
    {
        var hits = UrlExtractor.FindImageUrls("check this out https://example.com/foo.png");
        Assert.Single(hits);
        Assert.Equal("https://example.com/foo.png", hits[0].AbsoluteUri);
    }

    [Theory]
    [InlineData("https://x.com/a.jpg")]
    [InlineData("https://x.com/a.jpeg")]
    [InlineData("https://x.com/a.gif")]
    [InlineData("https://x.com/a.webp")]
    [InlineData("https://x.com/a.bmp")]
    [InlineData("HTTP://x.com/a.PNG")]
    public void Recognizes_supported_extensions(string url)
    {
        var hits = UrlExtractor.FindImageUrls($"see {url}");
        Assert.Single(hits);
    }

    [Fact]
    public void Ignores_non_image_urls()
    {
        var hits = UrlExtractor.FindImageUrls("see https://example.com/page.html");
        Assert.Empty(hits);
    }

    [Fact]
    public void Strips_trailing_sentence_punctuation()
    {
        var hits = UrlExtractor.FindImageUrls("seen at https://example.com/foo.png.");
        Assert.Single(hits);
        Assert.Equal("https://example.com/foo.png", hits[0].AbsoluteUri);
    }

    [Fact]
    public void Strips_trailing_closing_paren()
    {
        var hits = UrlExtractor.FindImageUrls("(see https://example.com/foo.png)");
        Assert.Single(hits);
        Assert.Equal("https://example.com/foo.png", hits[0].AbsoluteUri);
    }

    [Fact]
    public void Keeps_query_string()
    {
        var hits = UrlExtractor.FindImageUrls("avatar https://cdn.example.com/u.png?v=42&r=1");
        Assert.Single(hits);
        Assert.Equal("https://cdn.example.com/u.png?v=42&r=1", hits[0].AbsoluteUri);
    }

    [Fact]
    public void Finds_multiple_urls_in_one_message()
    {
        var hits = UrlExtractor.FindImageUrls(
            "before https://a.com/1.png middle https://b.com/2.jpg end");
        Assert.Equal(2, hits.Count);
    }

    [Fact]
    public void De_dupes_repeated_url_in_one_message()
    {
        var hits = UrlExtractor.FindImageUrls(
            "https://a.com/x.png and again https://a.com/x.png");
        Assert.Single(hits);
    }

    [Fact]
    public void Ignores_non_http_schemes()
    {
        var hits = UrlExtractor.FindImageUrls("file:///etc/passwd.png");
        Assert.Empty(hits);
    }

    [Fact]
    public void Empty_input_returns_empty()
    {
        Assert.Empty(UrlExtractor.FindImageUrls(string.Empty));
    }

    [Fact]
    public void Plain_text_with_dots_is_not_matched()
    {
        var hits = UrlExtractor.FindImageUrls("alice@example.com finished the work.");
        Assert.Empty(hits);
    }
}
