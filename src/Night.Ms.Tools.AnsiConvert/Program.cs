using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.Tools.AnsiConvert;

internal static class Program
{
    private const string Usage = """
        Night.Ms.Tools.AnsiConvert — convert PNG/JPEG to .ans half-block art.

        Usage:
          ansiconvert <input> [--width <cols>] [--depth truecolor|256|16] [--dither none|floyd] [--out <path>]

        Defaults:
          --width   80
          --depth   truecolor
          --dither  none for truecolor, floyd for 256 and 16
          --out     write to stdout
        """;

    private static int Main(string[] args)
    {
        if (args.Length == 0 || args[0] is "-h" or "--help")
        {
            Console.Error.WriteLine(Usage);
            return args.Length == 0 ? 1 : 0;
        }

        string? input = null;
        var width = 80;
        var depth = ColorDepth.Truecolor;
        DitherMode? dither = null;
        string? outPath = null;

        for (var i = 0; i < args.Length; i++)
        {
            var a = args[i];
            switch (a)
            {
                case "--width":
                    if (++i >= args.Length || !int.TryParse(args[i], out width) || width <= 0)
                        return Fail($"--width expects a positive integer (got {SafeNext(args, i)}).");
                    break;
                case "--depth":
                    if (++i >= args.Length) return Fail("--depth expects a value.");
                    depth = args[i].ToLowerInvariant() switch
                    {
                        "truecolor" or "tc" or "24" => ColorDepth.Truecolor,
                        "256" => ColorDepth.TwoFiftySix,
                        "16" => ColorDepth.Sixteen,
                        _ => throw new ArgumentException($"--depth must be truecolor, 256, or 16 (got {args[i]})."),
                    };
                    break;
                case "--dither":
                    if (++i >= args.Length) return Fail("--dither expects a value.");
                    dither = args[i].ToLowerInvariant() switch
                    {
                        "none" or "off" => DitherMode.None,
                        "floyd" or "fs" => DitherMode.FloydSteinberg,
                        _ => throw new ArgumentException($"--dither must be none or floyd (got {args[i]})."),
                    };
                    break;
                case "--out":
                    if (++i >= args.Length) return Fail("--out expects a path.");
                    outPath = args[i];
                    break;
                default:
                    if (input is null) input = a;
                    else return Fail($"Unexpected argument: {a}");
                    break;
            }
        }

        if (input is null) return Fail("missing input image path.");
        if (!File.Exists(input)) return Fail($"input does not exist: {input}");

        // Default dither: none for truecolor, floyd for the quantized depths.
        var effectiveDither = dither ?? (depth == ColorDepth.Truecolor ? DitherMode.None : DitherMode.FloydSteinberg);

        using var image = Image.Load<Rgba32>(input);
        var output = HalfBlockRenderer.Render(image, width, depth, effectiveDither);

        if (outPath is null)
        {
            Console.Out.Write(output);
        }
        else
        {
            File.WriteAllText(outPath, output);
        }
        return 0;
    }

    private static int Fail(string message)
    {
        Console.Error.WriteLine($"ansiconvert: {message}");
        Console.Error.WriteLine();
        Console.Error.WriteLine(Usage);
        return 1;
    }

    private static string SafeNext(string[] args, int i) => i < args.Length ? args[i] : "<end>";
}
