namespace Night.Ms.SshServer.Providers.Finance;

// Tiny block-glyph chart utilities used by FinanceScreen + FinanceDetailScreen.
//
// Sparkline.Render is the one-line inline chart: each output character represents one bucket
// from the input series, scaled into the 8-step block alphabet.
//
// BigChart.Render is the multi-row detail chart: each output column is one bucket painted
// using full + partial top-blocks so the chart reads as a continuous line.
public static class Sparkline
{
    // ▁ U+2581 .. █ U+2588 — eight ascending lower-block glyphs.
    private const string Blocks = "▁▂▃▄▅▆▇█";

    public static string Render(IReadOnlyList<double>? series, int width)
    {
        if (series is null || series.Count == 0 || width <= 0) return string.Empty;

        var buckets = Bucketize(series, width);
        if (buckets.Length == 0) return string.Empty;

        var min = double.PositiveInfinity;
        var max = double.NegativeInfinity;
        foreach (var v in buckets)
        {
            if (double.IsNaN(v) || double.IsInfinity(v)) continue;
            if (v < min) min = v;
            if (v > max) max = v;
        }
        if (double.IsInfinity(min)) return string.Empty;

        var range = max - min;
        var result = new char[buckets.Length];
        for (var i = 0; i < buckets.Length; i++)
        {
            var v = buckets[i];
            if (double.IsNaN(v) || double.IsInfinity(v))
            {
                result[i] = ' ';
                continue;
            }
            // Constant series → all bars at the lowest level so the user sees a flat line
            // rather than nothing.
            var t = range > 0 ? (v - min) / range : 0d;
            var idx = (int)Math.Round(t * (Blocks.Length - 1));
            if (idx < 0) idx = 0;
            else if (idx >= Blocks.Length) idx = Blocks.Length - 1;
            result[i] = Blocks[idx];
        }
        return new string(result);
    }

    // Bucketing strategy: when the series is longer than width, average values per bucket;
    // when it's shorter, repeat values so the chart fills the full width (small series still
    // look like a chart, not a stub). NaN/Infinity inputs are dropped from the average.
    private static double[] Bucketize(IReadOnlyList<double> series, int width)
    {
        var clean = new List<double>(series.Count);
        foreach (var v in series)
            if (!double.IsNaN(v) && !double.IsInfinity(v)) clean.Add(v);
        if (clean.Count == 0) return [];

        if (clean.Count <= width)
        {
            var outArr = new double[clean.Count];
            for (var i = 0; i < clean.Count; i++) outArr[i] = clean[i];
            return outArr;
        }

        var buckets = new double[width];
        var step = (double)clean.Count / width;
        for (var i = 0; i < width; i++)
        {
            var from = (int)Math.Floor(i * step);
            var to = (int)Math.Floor((i + 1) * step);
            if (to <= from) to = from + 1;
            if (to > clean.Count) to = clean.Count;
            var sum = 0d;
            var n = 0;
            for (var j = from; j < to; j++) { sum += clean[j]; n++; }
            buckets[i] = n > 0 ? sum / n : double.NaN;
        }
        return buckets;
    }
}

// Multi-row chart used by FinanceDetailScreen. Output is a list of strings, top row first.
// Each column shows the bucketed value as filled rows from the bottom, with the topmost row
// using a partial block glyph for sub-row resolution.
public static class BigChart
{
    private const string Blocks = "▁▂▃▄▅▆▇█";

    public static IReadOnlyList<string> Render(IReadOnlyList<double>? series, int width, int height)
    {
        if (series is null || series.Count == 0 || width <= 0 || height <= 0)
            return Enumerable.Repeat(string.Empty, Math.Max(0, height)).ToList();

        // Reuse Sparkline's bucketing — same min/max scan logic up here so the two charts
        // never disagree on which buckets are missing.
        var buckets = BucketAverages(series, width);
        if (buckets.Length == 0)
            return Enumerable.Repeat(new string(' ', width), height).ToList();

        var min = double.PositiveInfinity;
        var max = double.NegativeInfinity;
        foreach (var v in buckets)
        {
            if (double.IsNaN(v)) continue;
            if (v < min) min = v;
            if (v > max) max = v;
        }
        if (double.IsInfinity(min))
            return Enumerable.Repeat(new string(' ', width), height).ToList();

        var range = max - min;
        var rows = new char[height][];
        for (var r = 0; r < height; r++) { rows[r] = new char[width]; Array.Fill(rows[r], ' '); }

        for (var c = 0; c < buckets.Length; c++)
        {
            var v = buckets[c];
            if (double.IsNaN(v)) continue;
            var t = range > 0 ? (v - min) / range : 0.5;
            // Total fill in 1/8-row units, capped at the chart's full height.
            var subRows = (int)Math.Round(t * height * 8);
            if (subRows < 1) subRows = 1;
            if (subRows > height * 8) subRows = height * 8;

            var fullRows = subRows / 8;
            var partial = subRows % 8;

            for (var r = 0; r < fullRows; r++)
                rows[height - 1 - r][c] = '█';
            if (partial > 0 && fullRows < height)
                rows[height - 1 - fullRows][c] = Blocks[partial - 1];
        }

        var result = new string[height];
        for (var r = 0; r < height; r++) result[r] = new string(rows[r]);
        return result;
    }

    private static double[] BucketAverages(IReadOnlyList<double> series, int width)
    {
        var clean = new List<double>(series.Count);
        foreach (var v in series)
            if (!double.IsNaN(v) && !double.IsInfinity(v)) clean.Add(v);
        if (clean.Count == 0) return [];

        if (clean.Count <= width)
        {
            // Stretch a short series across `width` columns so the chart fills the box.
            var stretched = new double[width];
            for (var i = 0; i < width; i++)
            {
                var srcIdx = (int)Math.Floor((double)i * clean.Count / width);
                if (srcIdx >= clean.Count) srcIdx = clean.Count - 1;
                stretched[i] = clean[srcIdx];
            }
            return stretched;
        }

        var buckets = new double[width];
        var step = (double)clean.Count / width;
        for (var i = 0; i < width; i++)
        {
            var from = (int)Math.Floor(i * step);
            var to = (int)Math.Floor((i + 1) * step);
            if (to <= from) to = from + 1;
            if (to > clean.Count) to = clean.Count;
            var sum = 0d;
            var n = 0;
            for (var j = from; j < to; j++) { sum += clean[j]; n++; }
            buckets[i] = n > 0 ? sum / n : double.NaN;
        }
        return buckets;
    }
}
