using Night.Ms.Imaging;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.Tools.AnsiConvert.Tests;

public class HalfBlockRendererTests
{
    [Fact]
    public void Solid_red_image_renders_to_red_half_blocks_in_truecolor()
    {
        using var img = new Image<Rgba32>(4, 2, new Rgba32(255, 0, 0));

        var result = HalfBlockRenderer.Render(img, targetCols: 4, ColorDepth.Truecolor, DitherMode.None);

        Assert.Contains("38;2;255;0;0", result);
        Assert.Contains("48;2;255;0;0", result);
        Assert.Contains("▀", result);
        Assert.EndsWith("[0m\n", result);
    }

    [Fact]
    public void Output_has_target_column_count_of_glyphs_per_row()
    {
        using var img = new Image<Rgba32>(20, 10, new Rgba32(10, 20, 30));

        var result = HalfBlockRenderer.Render(img, targetCols: 20, ColorDepth.Truecolor, DitherMode.None);

        // One half-block per source column, on each output row. Strip ANSI and count ▀.
        var glyphs = result.Count(c => c == '▀');
        // 20 cols × (10/2) rows = 100 glyphs (image was already 10 tall; resize keeps it).
        Assert.True(glyphs >= 100, $"Expected at least 100 glyphs, got {glyphs}");
    }

    [Fact]
    public void Sixteen_color_output_uses_short_sgr_codes()
    {
        using var img = new Image<Rgba32>(2, 2, new Rgba32(255, 0, 0));

        var result = HalfBlockRenderer.Render(img, targetCols: 2, ColorDepth.Sixteen, DitherMode.None);

        // 16-color SGR codes are 30..37/90..97 for fg and 40..47/100..107 for bg.
        // Pure (255,0,0) is nearest the dark-red palette entry (170,0,0) → SGR 31/41,
        // not bright red (255,85,85). Both candidates use named-color SGR, never the
        // truecolor or 256-indexed escape forms.
        Assert.Contains("31;41", result);
        Assert.DoesNotContain("38;2;", result);
        Assert.DoesNotContain("38;5;", result);
    }

    [Fact]
    public void TwoFiftySix_output_uses_indexed_sgr_codes()
    {
        using var img = new Image<Rgba32>(2, 2, new Rgba32(255, 0, 0));

        var result = HalfBlockRenderer.Render(img, targetCols: 2, ColorDepth.TwoFiftySix, DitherMode.None);

        Assert.Contains("38;5;", result);
        Assert.Contains("48;5;", result);
        Assert.DoesNotContain("38;2;", result);
    }

    [Fact]
    public void Sgr_is_emitted_only_when_color_changes()
    {
        // Solid color across many columns → exactly one SGR for the row.
        using var img = new Image<Rgba32>(10, 2, new Rgba32(100, 150, 200));

        var result = HalfBlockRenderer.Render(img, targetCols: 10, ColorDepth.Truecolor, DitherMode.None);

        // Count the number of "[" inside escape sequences. Each SGR open contributes one;
        // the trailing reset [0m contributes one. So a row of uniform color should produce
        // exactly 2 escape sequences (one SGR + one reset).
        var rowCount = 1;
        var openCount = result.Split('').Length - 1;
        Assert.Equal(rowCount * 2, openCount);
    }

    [Fact]
    public void Floyd_steinberg_runs_without_throwing()
    {
        // A horizontal gradient gives the dither something to spread.
        using var img = new Image<Rgba32>(16, 4);
        img.ProcessPixelRows(rows =>
        {
            for (var y = 0; y < 4; y++)
            {
                var row = rows.GetRowSpan(y);
                for (var x = 0; x < 16; x++)
                {
                    var v = (byte)(x * 16);
                    row[x] = new Rgba32(v, v, v);
                }
            }
        });

        var result = HalfBlockRenderer.Render(img, targetCols: 16, ColorDepth.Sixteen, DitherMode.FloydSteinberg);

        Assert.Contains("▀", result);
    }
}
