using System.Security.Cryptography;
using System.Text;
using Night.Ms.SshServer.Tui.Chat;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Web;

// Procedural fallback avatar for users who haven't uploaded a real picture. Deterministic:
// the same handle (case-insensitively) always produces the same identicon. A 5x5 bit grid
// is sampled from the leading bytes of a SHA-1 of the lowercased handle, then mirrored
// left-right to give a symmetric 5-wide pattern (think GitHub's identicons). Primary fill
// color comes from HandleColorizer so the identicon matches the user's chat-handle color;
// background is the same dark neutral the rest of the BBS uses (#0a0e0f).
internal static class IdenticonRenderer
{
    private const int Tiles = 5;
    private const int Margin = 1; // tile-units of background border

    public static Image<Rgba32> Generate(string handle, int size = 256)
    {
        ArgumentOutOfRangeException.ThrowIfLessThan(size, 8);
        var normalized = (handle ?? string.Empty).ToLowerInvariant();
        var hash = SHA1.HashData(Encoding.UTF8.GetBytes(normalized)); // 20 bytes, plenty

        var fg = ToRgba(HandleColorizer.ColorFor(normalized));
        var bg = new Rgba32(0x0a, 0x0e, 0x0f);

        // Fill pattern: left half (3 columns including center) drawn from bits; right half
        // mirrors the left so the result is bilaterally symmetric.
        var leftCols = (Tiles + 1) / 2; // 3 for Tiles=5
        var pattern = new bool[Tiles, Tiles];
        for (var y = 0; y < Tiles; y++)
        {
            for (var x = 0; x < leftCols; x++)
            {
                var bitIndex = y * leftCols + x; // 0..14
                pattern[x, y] = ((hash[bitIndex] & 1) == 1);
                pattern[Tiles - 1 - x, y] = pattern[x, y];
            }
        }

        var image = new Image<Rgba32>(size, size, bg);
        // Tile size in pixels. Margin reserves a `Margin` tile-width border so the figure
        // never touches the edge — clean look at any rendering size.
        var totalTiles = Tiles + Margin * 2;
        var tilePx = size / totalTiles;
        var originPx = (size - tilePx * Tiles) / 2;

        // Drawing is direct-pixel-write rather than ImageSharp.Drawing.Processing.Fill so
        // we don't pull in a second NuGet for what's effectively 25 axis-aligned squares.
        image.ProcessPixelRows(accessor =>
        {
            for (var y = 0; y < Tiles; y++)
            {
                for (var x = 0; x < Tiles; x++)
                {
                    if (!pattern[x, y]) continue;
                    var x0 = originPx + x * tilePx;
                    var y0 = originPx + y * tilePx;
                    for (var py = 0; py < tilePx; py++)
                    {
                        var row = accessor.GetRowSpan(y0 + py);
                        for (var px = 0; px < tilePx; px++)
                        {
                            row[x0 + px] = fg;
                        }
                    }
                }
            }
        });
        return image;
    }

    private static Rgba32 ToRgba(Tui.Art.ArtColor c) => new(c.R, c.G, c.B);
}
