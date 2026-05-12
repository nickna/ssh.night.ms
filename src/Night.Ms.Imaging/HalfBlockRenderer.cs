using System.Text;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;
using SixLabors.ImageSharp.Processing;

namespace Night.Ms.Imaging;

public enum ColorDepth { Truecolor, TwoFiftySix, Sixteen }
public enum DitherMode { None, FloydSteinberg }

// Renders an image as ANSI half-block art: each terminal cell is "▀" (U+2580) with the
// foreground = top pixel color and background = bottom pixel color. Doubles vertical
// resolution and gives near-square effective pixels (terminal cells are ~2:1 tall).
public static class HalfBlockRenderer
{
    public static string Render(Image<Rgba32> source, int targetCols, ColorDepth depth, DitherMode dither)
    {
        if (targetCols <= 0) throw new ArgumentOutOfRangeException(nameof(targetCols));

        // We need 2 source pixels per output row, so resize to (cols, 2*rows). The source
        // aspect drives row count; sampler keeps detail without ringing on synthetic images.
        var resized = source.Clone(ctx =>
        {
            var origW = source.Width;
            var origH = source.Height;
            var newW = targetCols;
            var newH = Math.Max(2, (int)Math.Round(origH * (double)newW / origW));
            if (newH % 2 != 0) newH++; // even height so the half-block split is clean
            ctx.Resize(new ResizeOptions
            {
                Size = new Size(newW, newH),
                Sampler = KnownResamplers.Lanczos3,
                Mode = ResizeMode.Stretch,
            });
        });

        var pixels = ExtractRgb(resized);
        if (dither == DitherMode.FloydSteinberg && depth != ColorDepth.Truecolor)
        {
            ApplyFloydSteinberg(pixels, resized.Width, resized.Height, depth);
        }

        return EncodeCells(pixels, resized.Width, resized.Height, depth);
    }

    private static Rgb[,] ExtractRgb(Image<Rgba32> img)
    {
        var w = img.Width;
        var h = img.Height;
        var result = new Rgb[h, w];
        img.ProcessPixelRows(rows =>
        {
            for (var y = 0; y < h; y++)
            {
                var row = rows.GetRowSpan(y);
                for (var x = 0; x < w; x++)
                {
                    var p = row[x];
                    result[y, x] = new Rgb(p.R, p.G, p.B);
                }
            }
        });
        return result;
    }

    // Floyd–Steinberg error diffusion against the target palette. Only meaningful for the
    // quantized depths — truecolor renders the source RGB verbatim.
    private static void ApplyFloydSteinberg(Rgb[,] pixels, int width, int height, ColorDepth depth)
    {
        // Work in float to accumulate error fractions.
        var work = new float[height, width, 3];
        for (var y = 0; y < height; y++)
            for (var x = 0; x < width; x++)
            {
                work[y, x, 0] = pixels[y, x].R;
                work[y, x, 1] = pixels[y, x].G;
                work[y, x, 2] = pixels[y, x].B;
            }

        for (var y = 0; y < height; y++)
        {
            for (var x = 0; x < width; x++)
            {
                var oldR = work[y, x, 0];
                var oldG = work[y, x, 1];
                var oldB = work[y, x, 2];
                var quant = Quantize(new Rgb(ToByte(oldR), ToByte(oldG), ToByte(oldB)), depth);
                pixels[y, x] = quant;
                var errR = oldR - quant.R;
                var errG = oldG - quant.G;
                var errB = oldB - quant.B;

                Spread(work, x + 1, y,     7f / 16f, errR, errG, errB, width, height);
                Spread(work, x - 1, y + 1, 3f / 16f, errR, errG, errB, width, height);
                Spread(work, x,     y + 1, 5f / 16f, errR, errG, errB, width, height);
                Spread(work, x + 1, y + 1, 1f / 16f, errR, errG, errB, width, height);
            }
        }
    }

    private static void Spread(float[,,] work, int x, int y, float weight, float er, float eg, float eb, int w, int h)
    {
        if (x < 0 || x >= w || y < 0 || y >= h) return;
        work[y, x, 0] += er * weight;
        work[y, x, 1] += eg * weight;
        work[y, x, 2] += eb * weight;
    }

    private static byte ToByte(float v) => (byte)Math.Clamp((int)Math.Round(v), 0, 255);

    private static Rgb Quantize(Rgb pixel, ColorDepth depth) => depth switch
    {
        ColorDepth.Truecolor => pixel,
        ColorDepth.TwoFiftySix => Palette.TwoFiftySix[Palette.NearestIndex(pixel, Palette.TwoFiftySix)],
        ColorDepth.Sixteen => Palette.Sixteen[Palette.NearestIndex(pixel, Palette.Sixteen)],
        _ => pixel,
    };

    private static string EncodeCells(Rgb[,] pixels, int width, int height, ColorDepth depth)
    {
        var rows = height / 2;
        var sb = new StringBuilder();
        const string ESC = "";
        Rgb? lastFg = null;
        Rgb? lastBg = null;

        for (var row = 0; row < rows; row++)
        {
            lastFg = null;
            lastBg = null;
            for (var x = 0; x < width; x++)
            {
                var top = pixels[row * 2, x];
                var bot = pixels[row * 2 + 1, x];

                // Emit SGR only when fg or bg actually changed since the previous cell.
                if (!Equal(top, lastFg) || !Equal(bot, lastBg))
                {
                    var fgCode = SgrCode(top, foreground: true, depth);
                    var bgCode = SgrCode(bot, foreground: false, depth);
                    sb.Append($"{ESC}[{fgCode};{bgCode}m");
                    lastFg = top;
                    lastBg = bot;
                }
                sb.Append('▀'); // ▀ UPPER HALF BLOCK
            }
            sb.Append($"{ESC}[0m\n");
        }
        return sb.ToString();
    }

    private static bool Equal(Rgb a, Rgb? b) => b is { } v && a.R == v.R && a.G == v.G && a.B == v.B;

    private static string SgrCode(Rgb c, bool foreground, ColorDepth depth) => depth switch
    {
        ColorDepth.Truecolor => $"{(foreground ? 38 : 48)};2;{c.R};{c.G};{c.B}",
        ColorDepth.TwoFiftySix => $"{(foreground ? 38 : 48)};5;{Palette.NearestIndex(c, Palette.TwoFiftySix)}",
        ColorDepth.Sixteen => foreground
            ? Palette.SgrForeground(Palette.NearestIndex(c, Palette.Sixteen)).ToString()
            : Palette.SgrBackground(Palette.NearestIndex(c, Palette.Sixteen)).ToString(),
        _ => "0",
    };
}
