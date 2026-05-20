using System.Text;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tests;

public class SgrParserTests
{
    private const string ESC = "";

    [Fact]
    public void Plain_text_produces_single_row_grid()
    {
        var grid = SgrParser.Parse("hi");

        Assert.Equal(2, grid.Width);
        Assert.Equal(1, grid.Height);
        Assert.Equal(new Rune('h'), grid[0, 0].Glyph);
        Assert.Equal(new Rune('i'), grid[1, 0].Glyph);
    }

    [Fact]
    public void Newlines_advance_rows()
    {
        var grid = SgrParser.Parse("ab\ncde");

        Assert.Equal(3, grid.Width);
        Assert.Equal(2, grid.Height);
        Assert.Equal(new Rune('a'), grid[0, 0].Glyph);
        Assert.Equal(new Rune('c'), grid[0, 1].Glyph);
        // Short row right-pads with empty cells.
        Assert.Equal(new Rune(' '), grid[2, 0].Glyph);
    }

    [Fact]
    public void Sixteen_color_foreground_sets_fg()
    {
        var grid = SgrParser.Parse($"{ESC}[31mr");

        Assert.Equal(SgrParser.AnsiBasic(1), grid[0, 0].Foreground);
    }

    [Fact]
    public void Sixteen_color_bright_foreground_sets_fg()
    {
        var grid = SgrParser.Parse($"{ESC}[91mR");

        Assert.Equal(SgrParser.AnsiBright(1), grid[0, 0].Foreground);
    }

    [Fact]
    public void Sixteen_color_background_sets_bg()
    {
        var grid = SgrParser.Parse($"{ESC}[44mb");

        Assert.Equal(SgrParser.AnsiBasic(4), grid[0, 0].Background);
    }

    [Fact]
    public void Bold_style_is_applied_and_cleared()
    {
        var grid = SgrParser.Parse($"{ESC}[1mB{ESC}[22mp");

        Assert.True(grid[0, 0].Style.HasFlag(ArtStyle.Bold));
        Assert.False(grid[1, 0].Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Reset_restores_defaults()
    {
        var grid = SgrParser.Parse($"{ESC}[31;1mR{ESC}[0mN");

        Assert.Equal(SgrParser.AnsiBasic(1), grid[0, 0].Foreground);
        Assert.True(grid[0, 0].Style.HasFlag(ArtStyle.Bold));
        Assert.Equal(ArtColor.DefaultForeground, grid[1, 0].Foreground);
        Assert.False(grid[1, 0].Style.HasFlag(ArtStyle.Bold));
    }

    [Fact]
    public void Empty_sgr_is_reset()
    {
        var grid = SgrParser.Parse($"{ESC}[31mR{ESC}[mN");

        Assert.Equal(SgrParser.AnsiBasic(1), grid[0, 0].Foreground);
        Assert.Equal(ArtColor.DefaultForeground, grid[1, 0].Foreground);
    }

    [Fact]
    public void Truecolor_foreground_sets_rgb()
    {
        var grid = SgrParser.Parse($"{ESC}[38;2;200;100;50mt");

        Assert.Equal(new ArtColor(200, 100, 50), grid[0, 0].Foreground);
    }

    [Fact]
    public void Truecolor_background_sets_rgb()
    {
        var grid = SgrParser.Parse($"{ESC}[48;2;10;20;30mt");

        Assert.Equal(new ArtColor(10, 20, 30), grid[0, 0].Background);
    }

    [Fact]
    public void Xterm256_grayscale_maps_to_rgb()
    {
        // Index 232 is the start of the grayscale ramp: 8,8,8.
        var grid = SgrParser.Parse($"{ESC}[38;5;232mg");

        Assert.Equal(new ArtColor(8, 8, 8), grid[0, 0].Foreground);
    }

    [Fact]
    public void Xterm256_cube_maps_to_rgb()
    {
        // Index 196 = (5,0,0) in the cube → (255, 0, 0).
        var grid = SgrParser.Parse($"{ESC}[38;5;196mc");

        Assert.Equal(new ArtColor(255, 0, 0), grid[0, 0].Foreground);
    }

    [Fact]
    public void Xterm256_first_sixteen_use_named_palette()
    {
        var grid = SgrParser.Parse($"{ESC}[38;5;9mB");

        // Index 9 = bright red.
        Assert.Equal(SgrParser.AnsiBright(1), grid[0, 0].Foreground);
    }

    [Fact]
    public void Compound_sgr_applies_all_params()
    {
        var grid = SgrParser.Parse($"{ESC}[1;31;44mx");

        Assert.True(grid[0, 0].Style.HasFlag(ArtStyle.Bold));
        Assert.Equal(SgrParser.AnsiBasic(1), grid[0, 0].Foreground);
        Assert.Equal(SgrParser.AnsiBasic(4), grid[0, 0].Background);
    }

    [Fact]
    public void Unknown_csi_is_skipped_without_emitting_cells()
    {
        // \e[2J is "clear screen" — not SGR. Parser should consume it and continue.
        var grid = SgrParser.Parse($"{ESC}[2Jhi");

        Assert.Equal(2, grid.Width);
        Assert.Equal(new Rune('h'), grid[0, 0].Glyph);
    }

    [Fact]
    public void Truncated_escape_does_not_throw()
    {
        var grid = SgrParser.Parse($"abc{ESC}[38;2;1");

        Assert.Equal(3, grid.Width);
        Assert.Equal(1, grid.Height);
    }

    [Fact]
    public void Cr_resets_current_row()
    {
        var grid = SgrParser.Parse("ab\rcd");

        Assert.Equal(new Rune('c'), grid[0, 0].Glyph);
        Assert.Equal(new Rune('d'), grid[1, 0].Glyph);
        Assert.Equal(1, grid.Height);
    }

    [Fact]
    public void Crlf_is_one_newline_not_row_reset()
    {
        var grid = SgrParser.Parse("ab\r\ncd");

        Assert.Equal(2, grid.Height);
        Assert.Equal(new Rune('a'), grid[0, 0].Glyph);
        Assert.Equal(new Rune('b'), grid[1, 0].Glyph);
        Assert.Equal(new Rune('c'), grid[0, 1].Glyph);
        Assert.Equal(new Rune('d'), grid[1, 1].Glyph);
    }

    [Fact]
    public void Trailing_newline_is_trimmed()
    {
        var grid = SgrParser.Parse("one\ntwo\n");

        Assert.Equal(2, grid.Height);
    }

    [Fact]
    public void Default_fg_and_bg_codes_restore_defaults_individually()
    {
        var grid = SgrParser.Parse($"{ESC}[31;44mA{ESC}[39mB{ESC}[49mC");

        Assert.Equal(SgrParser.AnsiBasic(1), grid[0, 0].Foreground);
        Assert.Equal(SgrParser.AnsiBasic(4), grid[0, 0].Background);

        Assert.Equal(ArtColor.DefaultForeground, grid[1, 0].Foreground);
        Assert.Equal(SgrParser.AnsiBasic(4), grid[1, 0].Background);

        Assert.Equal(ArtColor.DefaultForeground, grid[2, 0].Foreground);
        Assert.Equal(ArtColor.DefaultBackground, grid[2, 0].Background);
    }

    [Fact]
    public void Half_block_glyph_is_preserved()
    {
        var grid = SgrParser.Parse("▀"); // ▀ U+2580 UPPER HALF BLOCK

        Assert.Equal(new Rune(0x2580), grid[0, 0].Glyph);
    }
}
