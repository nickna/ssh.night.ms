using Night.Ms.SshServer.Providers.Finance;

namespace Night.Ms.SshServer.Tests.Providers.Finance;

public class SparklineTests
{
    [Fact]
    public void Null_series_returns_empty()
    {
        Assert.Equal(string.Empty, Sparkline.Render(null, 10));
    }

    [Fact]
    public void Empty_series_returns_empty()
    {
        Assert.Equal(string.Empty, Sparkline.Render(Array.Empty<double>(), 10));
    }

    [Fact]
    public void Zero_width_returns_empty()
    {
        Assert.Equal(string.Empty, Sparkline.Render(new[] { 1.0, 2.0 }, 0));
    }

    [Fact]
    public void Single_point_renders_one_glyph()
    {
        var s = Sparkline.Render(new[] { 42.0 }, 10);
        Assert.Single(s);
        // Constant series → lowest block (▁).
        Assert.Equal('▁', s[0]);
    }

    [Fact]
    public void Constant_series_renders_all_lowest_blocks()
    {
        var s = Sparkline.Render(new[] { 5.0, 5.0, 5.0, 5.0 }, 4);
        Assert.Equal("▁▁▁▁", s);
    }

    [Fact]
    public void Ascending_series_spans_block_alphabet()
    {
        var s = Sparkline.Render(new[] { 1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0 }, 8);
        // First and last should be the extremes of the alphabet.
        Assert.Equal('▁', s[0]);
        Assert.Equal('█', s[^1]);
    }

    [Fact]
    public void Short_series_does_not_pad_with_extra_glyphs()
    {
        // A 3-point series at width 10 still returns 3 glyphs — the chart shows what it has
        // without inventing buckets.
        var s = Sparkline.Render(new[] { 1.0, 2.0, 3.0 }, 10);
        Assert.Equal(3, s.Length);
    }

    [Fact]
    public void Long_series_buckets_down_to_requested_width()
    {
        var series = Enumerable.Range(0, 100).Select(i => (double)i).ToArray();
        var s = Sparkline.Render(series, 5);
        Assert.Equal(5, s.Length);
        Assert.Equal('▁', s[0]);
        Assert.Equal('█', s[^1]);
    }

    [Fact]
    public void NaN_values_are_dropped_from_scaling()
    {
        var s = Sparkline.Render(new[] { 1.0, double.NaN, 2.0, 3.0 }, 4);
        // Output length matches the non-NaN count (NaN gets dropped in cleaning).
        Assert.Equal(3, s.Length);
        Assert.Equal('▁', s[0]);
        Assert.Equal('█', s[^1]);
    }
}

public class BigChartTests
{
    [Fact]
    public void Empty_series_returns_blank_rows()
    {
        var rows = BigChart.Render(Array.Empty<double>(), 10, 4);
        Assert.Equal(4, rows.Count);
        Assert.All(rows, r => Assert.Equal(string.Empty, r));
    }

    [Fact]
    public void Null_series_returns_blank_rows_at_requested_height()
    {
        var rows = BigChart.Render(null, 10, 3);
        Assert.Equal(3, rows.Count);
    }

    [Fact]
    public void Zero_height_returns_zero_rows()
    {
        var rows = BigChart.Render(new[] { 1.0, 2.0 }, 10, 0);
        Assert.Empty(rows);
    }

    [Fact]
    public void Ascending_series_paints_taller_columns_to_the_right()
    {
        var rows = BigChart.Render(new[] { 1.0, 2.0, 3.0, 4.0 }, 4, 8);
        // The bottom row should have '█' (or similar full block) under every column since
        // every column has at least one row of fill.
        var bottom = rows[^1];
        Assert.Equal(4, bottom.Length);
        Assert.All(bottom, c => Assert.NotEqual(' ', c));
        // The first column (lowest value) should be empty on the top row; the last column
        // (highest value) should be painted on the top row.
        var top = rows[0];
        Assert.Equal(' ', top[0]);
        Assert.NotEqual(' ', top[^1]);
    }
}
