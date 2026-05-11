using System.Buffers;
using System.Text;

namespace Night.Ms.SshServer.Tui.Art;

// Parses ANSI text with SGR escape sequences into a CellGrid. Supports the subset our
// AnsiConvert tool emits: reset (0), bold on/off (1/22), 16-color fg/bg (30-37, 90-97,
// 40-47, 100-107), default fg/bg (39/49), 256-color (38;5;N / 48;5;N), and truecolor
// (38;2;R;G;B / 48;2;R;G;B). Unknown CSI sequences are tolerated (consumed, ignored) so
// a stray cursor-position code in a 3rd-party file doesn't break loading.
internal static class SgrParser
{
    public static CellGrid Parse(string input)
    {
        ArgumentNullException.ThrowIfNull(input);

        var rows = new List<List<Cell>> { new() };
        var fg = ArtColor.DefaultForeground;
        var bg = ArtColor.DefaultBackground;
        var style = ArtStyle.None;

        var span = input.AsSpan();
        var i = 0;
        while (i < span.Length)
        {
            var ch = span[i];

            if (ch == '' && i + 1 < span.Length && span[i + 1] == '[')
            {
                var paramsStart = i + 2;
                var j = paramsStart;
                while (j < span.Length && !IsCsiFinal(span[j])) j++;
                if (j >= span.Length) break; // truncated escape — stop, don't fail
                var finalByte = span[j];
                if (finalByte == 'm')
                {
                    ApplySgr(span[paramsStart..j], ref fg, ref bg, ref style);
                }
                // else: ignore (cursor movement, mode setting, etc.)
                i = j + 1;
                continue;
            }

            if (ch == '\r')
            {
                rows[^1].Clear();
                i++;
                continue;
            }

            if (ch == '\n')
            {
                rows.Add(new List<Cell>());
                i++;
                continue;
            }

            if (Rune.DecodeFromUtf16(span[i..], out var rune, out var consumed) == OperationStatus.Done)
            {
                rows[^1].Add(new Cell(rune, fg, bg, style));
                i += consumed;
            }
            else
            {
                i++; // skip a malformed unit
            }
        }

        if (rows.Count > 1 && rows[^1].Count == 0) rows.RemoveAt(rows.Count - 1);

        var width = 0;
        foreach (var row in rows) if (row.Count > width) width = row.Count;
        var height = rows.Count;

        var grid = new CellGrid(width, height);
        for (var y = 0; y < height; y++)
        {
            var row = rows[y];
            for (var x = 0; x < row.Count; x++)
            {
                grid[x, y] = row[x];
            }
        }
        return grid;
    }

    private static bool IsCsiFinal(char c) => c >= 0x40 && c <= 0x7e;

    private static void ApplySgr(ReadOnlySpan<char> parameters, ref ArtColor fg, ref ArtColor bg, ref ArtStyle style)
    {
        if (parameters.IsEmpty)
        {
            fg = ArtColor.DefaultForeground;
            bg = ArtColor.DefaultBackground;
            style = ArtStyle.None;
            return;
        }

        Span<int> codes = stackalloc int[32];
        var count = 0;
        var idx = 0;
        while (idx < parameters.Length && count < codes.Length)
        {
            var end = idx;
            while (end < parameters.Length && parameters[end] != ';') end++;
            var slice = parameters[idx..end];
            if (slice.IsEmpty)
            {
                codes[count++] = 0;
            }
            else if (int.TryParse(slice, out var n))
            {
                codes[count++] = n;
            }
            idx = end + 1;
        }

        var k = 0;
        while (k < count)
        {
            var code = codes[k];
            switch (code)
            {
                case 0:
                    fg = ArtColor.DefaultForeground;
                    bg = ArtColor.DefaultBackground;
                    style = ArtStyle.None;
                    break;
                case 1:
                    style |= ArtStyle.Bold;
                    break;
                case 22:
                    style &= ~ArtStyle.Bold;
                    break;
                case >= 30 and <= 37:
                    fg = AnsiBasic(code - 30);
                    break;
                case 38:
                    if (k + 2 < count && codes[k + 1] == 5)
                    {
                        fg = FromXterm256(codes[k + 2]);
                        k += 2;
                    }
                    else if (k + 4 < count && codes[k + 1] == 2)
                    {
                        fg = FromRgb(codes[k + 2], codes[k + 3], codes[k + 4]);
                        k += 4;
                    }
                    break;
                case 39:
                    fg = ArtColor.DefaultForeground;
                    break;
                case >= 40 and <= 47:
                    bg = AnsiBasic(code - 40);
                    break;
                case 48:
                    if (k + 2 < count && codes[k + 1] == 5)
                    {
                        bg = FromXterm256(codes[k + 2]);
                        k += 2;
                    }
                    else if (k + 4 < count && codes[k + 1] == 2)
                    {
                        bg = FromRgb(codes[k + 2], codes[k + 3], codes[k + 4]);
                        k += 4;
                    }
                    break;
                case 49:
                    bg = ArtColor.DefaultBackground;
                    break;
                case >= 90 and <= 97:
                    fg = AnsiBright(code - 90);
                    break;
                case >= 100 and <= 107:
                    bg = AnsiBright(code - 100);
                    break;
            }
            k++;
        }
    }

    // Standard xterm 16-color palette RGB values. The bright row matches xterm's "bold"
    // palette; chosen because most modern terminals (Windows Terminal, iTerm2, kitty) render
    // these RGB values when given the corresponding SGR code.
    public static ArtColor AnsiBasic(int n) => n switch
    {
        0 => new(0, 0, 0),         // black
        1 => new(170, 0, 0),       // red
        2 => new(0, 170, 0),       // green
        3 => new(170, 85, 0),      // yellow (dark / amber)
        4 => new(0, 0, 170),       // blue
        5 => new(170, 0, 170),     // magenta
        6 => new(0, 170, 170),     // cyan
        7 => new(170, 170, 170),   // gray (light gray)
        _ => ArtColor.DefaultForeground,
    };

    public static ArtColor AnsiBright(int n) => n switch
    {
        0 => new(85, 85, 85),      // dark gray (bright black)
        1 => new(255, 85, 85),     // bright red
        2 => new(85, 255, 85),     // bright green
        3 => new(255, 255, 85),    // bright yellow
        4 => new(85, 85, 255),     // bright blue
        5 => new(255, 85, 255),    // bright magenta
        6 => new(85, 255, 255),    // bright cyan
        7 => new(255, 255, 255),   // white
        _ => ArtColor.DefaultForeground,
    };

    public static ArtColor FromXterm256(int n)
    {
        if (n is < 0 or > 255) return ArtColor.DefaultForeground;
        if (n < 8) return AnsiBasic(n);
        if (n < 16) return AnsiBright(n - 8);
        if (n >= 232)
        {
            var gray = 8 + 10 * (n - 232);
            return FromRgb(gray, gray, gray);
        }
        var i = n - 16;
        var r = (i / 36) % 6;
        var g = (i / 6) % 6;
        var b = i % 6;
        return FromRgb(CubeLevel(r), CubeLevel(g), CubeLevel(b));
    }

    private static int CubeLevel(int v) => v switch
    {
        0 => 0,
        1 => 95,
        2 => 135,
        3 => 175,
        4 => 215,
        _ => 255,
    };

    public static ArtColor FromRgb(int r, int g, int b) =>
        new((byte)Clamp(r), (byte)Clamp(g), (byte)Clamp(b));

    private static int Clamp(int v) => v < 0 ? 0 : v > 255 ? 255 : v;
}
