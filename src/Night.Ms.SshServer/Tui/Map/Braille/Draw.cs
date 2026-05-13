using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui.Map.Braille;

// Drawing primitives onto a BrailleCanvas. All operations take canvas-subpixel coordinates
// (so polygon callers project tile coords → subpixels first) and clip to the canvas bounds.
//
// Polygon fill is two-pass: rasterise scanline-by-scanline into a private bool[,] mask,
// then translate cell-by-cell. The two-pass design lets us call FillCell on cells whose
// dots are all inside the polygon — the canvas paints those as a solid block rather than
// a 99%-filled braille glyph, which reads much better on a typical terminal renderer.
internal static class Draw
{
    // Bresenham line. SetPixel handles clipping per-dot so we don't need a Cohen-Sutherland
    // pre-pass — at subpixel resolution the clipper would visit a couple-hundred dots in
    // the worst case anyway.
    public static void DrawLine(BrailleCanvas canvas, int x0, int y0, int x1, int y1, ArtColor color)
    {
        var dx =  Math.Abs(x1 - x0);
        var dy = -Math.Abs(y1 - y0);
        var sx = x0 < x1 ? 1 : -1;
        var sy = y0 < y1 ? 1 : -1;
        var err = dx + dy;
        var x = x0;
        var y = y0;
        while (true)
        {
            canvas.SetPixel(x, y, color);
            if (x == x1 && y == y1) break;
            var e2 = 2 * err;
            if (e2 >= dy)
            {
                if (x == x1) break;
                err += dy;
                x += sx;
            }
            if (e2 <= dx)
            {
                if (y == y1) break;
                err += dx;
                y += sy;
            }
        }
    }

    public static void DrawLineStrip(BrailleCanvas canvas, ReadOnlySpan<(int X, int Y)> points, ArtColor color)
    {
        for (var i = 1; i < points.Length; i++)
        {
            DrawLine(canvas, points[i - 1].X, points[i - 1].Y, points[i].X, points[i].Y, color);
        }
    }

    // Even-odd polygon fill. `rings` is a list of closed loops; the first is the outer ring,
    // the rest are holes. The MVT spec carries ring winding to distinguish outer from hole,
    // but we just trust even-odd over all rings — simpler and produces the same image for
    // any planar polygon.
    public static void FillPolygon(
        BrailleCanvas canvas,
        IReadOnlyList<IReadOnlyList<(int X, int Y)>> rings,
        ArtColor color)
    {
        if (rings.Count == 0) return;

        var w = canvas.PixelWidth;
        var h = canvas.PixelHeight;
        if (w == 0 || h == 0) return;

        // Bound the scan by the polygon's actual Y range — most water features cover only a
        // sliver of the viewport, so scanning the whole canvas would burn 90% of the work
        // outside the geometry.
        var minY = int.MaxValue;
        var maxY = int.MinValue;
        foreach (var ring in rings)
        {
            foreach (var (_, y) in ring)
            {
                if (y < minY) minY = y;
                if (y > maxY) maxY = y;
            }
        }
        if (maxY < 0 || minY >= h) return;
        minY = Math.Max(0, minY);
        maxY = Math.Min(h - 1, maxY);

        var mask = new bool[h, w];
        var crossings = new List<double>(16);

        for (var y = minY; y <= maxY; y++)
        {
            crossings.Clear();
            var scanY = y + 0.5; // sample at the middle of each subpixel row
            foreach (var ring in rings)
            {
                var n = ring.Count;
                if (n < 2) continue;
                for (var i = 0; i < n; i++)
                {
                    var (ax, ay) = ring[i];
                    var (bx, by) = ring[(i + 1) % n];
                    if (ay == by) continue; // horizontal edge contributes no crossing
                    var lo = Math.Min(ay, by);
                    var hi = Math.Max(ay, by);
                    // Half-open interval [lo, hi) — keeps a vertex shared by two edges from
                    // double-counting under the even-odd rule.
                    if (scanY < lo || scanY >= hi) continue;
                    var t = (scanY - ay) / (double)(by - ay);
                    var ix = ax + t * (bx - ax);
                    crossings.Add(ix);
                }
            }
            if (crossings.Count < 2) continue;
            crossings.Sort();
            for (var i = 0; i + 1 < crossings.Count; i += 2)
            {
                var xStart = (int)Math.Ceiling(crossings[i]);
                var xEnd   = (int)Math.Floor(crossings[i + 1]);
                if (xEnd < 0 || xStart >= w) continue;
                xStart = Math.Max(0, xStart);
                xEnd   = Math.Min(w - 1, xEnd);
                for (var x = xStart; x <= xEnd; x++) mask[y, x] = true;
            }
        }

        // Translate the subpixel mask into canvas ops. A cell where every dot is inside the
        // polygon goes through FillCell (solid block); a partial cell becomes a sequence of
        // SetPixel calls so the polygon edge anti-aliases via braille texture.
        var cellCols = canvas.CellCols;
        var cellRows = canvas.CellRows;
        for (var cy = 0; cy < cellRows; cy++)
        {
            for (var cx = 0; cx < cellCols; cx++)
            {
                var baseX = cx * BrailleCanvas.SubPixelsX;
                var baseY = cy * BrailleCanvas.SubPixelsY;
                var count = 0;
                for (var dy = 0; dy < BrailleCanvas.SubPixelsY; dy++)
                {
                    for (var dx = 0; dx < BrailleCanvas.SubPixelsX; dx++)
                    {
                        if (mask[baseY + dy, baseX + dx]) count++;
                    }
                }
                if (count == 0) continue;
                if (count == BrailleCanvas.SubPixelsX * BrailleCanvas.SubPixelsY)
                {
                    canvas.FillCell(cx, cy, color);
                    continue;
                }
                for (var dy = 0; dy < BrailleCanvas.SubPixelsY; dy++)
                {
                    for (var dx = 0; dx < BrailleCanvas.SubPixelsX; dx++)
                    {
                        if (mask[baseY + dy, baseX + dx]) canvas.SetPixel(baseX + dx, baseY + dy, color);
                    }
                }
            }
        }
    }

    public static void DrawString(BrailleCanvas canvas, int cellX, int cellY, string text, ArtColor color)
    {
        if (string.IsNullOrEmpty(text)) return;
        var x = cellX;
        foreach (var rune in text.EnumerateRunes())
        {
            if (x >= canvas.CellCols) break;
            // Skip control + zero-width runes; pass everything else through as-is. Wide CJK
            // runes will get cut off at one cell — acceptable for v1 since we filter labels
            // to name_en / name:en when available.
            if (rune.Value < 0x20 || rune.Value == 0x7F) continue;
            if (x >= 0 && cellY >= 0 && cellY < canvas.CellRows)
            {
                canvas.DrawText(x, cellY, rune, color);
            }
            x++;
        }
    }
}
