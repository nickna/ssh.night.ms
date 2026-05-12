namespace Night.Ms.Imaging;

// Color tables used when quantizing source pixels for the 16- and 256-color depths.
// The 16-color palette mirrors the standard xterm colors and intentionally matches the
// values that Night.Ms.SshServer.Tui.Art.SgrParser maps SGR 30-37/90-97 back to, so a
// round-trip (rendered .ans → parsed CellGrid → painted via Terminal.Gui) preserves
// chromatic intent.
public static class Palette
{
    // Standard xterm 16-color palette. Dark row first (0..7), bright row second (8..15).
    public static readonly Rgb[] Sixteen =
    [
        new(0, 0, 0),         // 0 black
        new(170, 0, 0),       // 1 red
        new(0, 170, 0),       // 2 green
        new(170, 85, 0),      // 3 yellow
        new(0, 0, 170),       // 4 blue
        new(170, 0, 170),     // 5 magenta
        new(0, 170, 170),     // 6 cyan
        new(170, 170, 170),   // 7 gray
        new(85, 85, 85),      // 8 dark gray (bright black)
        new(255, 85, 85),     // 9 bright red
        new(85, 255, 85),     // 10 bright green
        new(255, 255, 85),    // 11 bright yellow
        new(85, 85, 255),     // 12 bright blue
        new(255, 85, 255),    // 13 bright magenta
        new(85, 255, 255),    // 14 bright cyan
        new(255, 255, 255),   // 15 white
    ];

    // SGR foreground codes for the 16 indices. Background = +10. Bright colors use the 9x
    // series (90..97 fg, 100..107 bg).
    public static int SgrForeground(int index16) => index16 < 8 ? 30 + index16 : 90 + (index16 - 8);
    public static int SgrBackground(int index16) => index16 < 8 ? 40 + index16 : 100 + (index16 - 8);

    private static readonly Rgb[] _twoFiftySix = BuildTwoFiftySix();

    public static IReadOnlyList<Rgb> TwoFiftySix => _twoFiftySix;

    private static Rgb[] BuildTwoFiftySix()
    {
        var palette = new Rgb[256];
        for (var i = 0; i < 16; i++) palette[i] = Sixteen[i];

        // 6x6x6 cube — index 16..231. Channel levels: 0, 95, 135, 175, 215, 255.
        ReadOnlySpan<byte> levels = [0, 95, 135, 175, 215, 255];
        for (var n = 16; n < 232; n++)
        {
            var i = n - 16;
            var r = (i / 36) % 6;
            var g = (i / 6) % 6;
            var b = i % 6;
            palette[n] = new Rgb(levels[r], levels[g], levels[b]);
        }

        // Grayscale ramp — index 232..255. Levels 8, 18, 28, ... 238.
        for (var n = 232; n < 256; n++)
        {
            var v = (byte)(8 + 10 * (n - 232));
            palette[n] = new Rgb(v, v, v);
        }
        return palette;
    }

    public static int NearestIndex(Rgb pixel, IReadOnlyList<Rgb> palette)
    {
        var best = 0;
        var bestDist = int.MaxValue;
        for (var i = 0; i < palette.Count; i++)
        {
            var c = palette[i];
            var dr = pixel.R - c.R;
            var dg = pixel.G - c.G;
            var db = pixel.B - c.B;
            var dist = dr * dr + dg * dg + db * db;
            if (dist < bestDist)
            {
                bestDist = dist;
                best = i;
                if (dist == 0) break;
            }
        }
        return best;
    }
}
