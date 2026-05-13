using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tui;

// Provides the banner shown at the top of RegisterScreen + LobbyScreen.
//
// Resolves a path via NIGHTMS_LOGIN_ART_PATH (or LoginArt:Path config key). Files ending in
// `.ans` are parsed as ANSI-escape text into a CellGrid for full-color rendering via
// AnsiArtView; everything else loads as a monochrome string that the screens can hand to a
// plain Label. Falls back to the built-in DefaultArt on any failure so a missing or
// malformed file never blocks a session from rendering.
public sealed class ArtProvider
{
    public const string DefaultArt =
        " ╓──────────────────────────────────────────────────╖\n" +
        " ║                                                  ║\n" +
        " ║   ssh.night.ms   ▒▓█  a small bbs over ssh  █▓▒  ║\n" +
        " ║                                                  ║\n" +
        " ╙──────────────────────────────────────────────────╜";

    // Monochrome text fallback. Only meaningful when IsColor is false — when a .ans file
    // parsed successfully, this is set to DefaultArt as a "if you ignore IsColor, here's a
    // non-empty placeholder so you don't crash" safety net; the real content is in Grid.
    // Branch on IsColor in the caller — every screen does.
    public string Art { get; }
    // Display rows of the rendered banner — Grid.Height when IsColor, line count of Art otherwise.
    public int LineCount { get; }

    // Non-null when the configured asset parsed successfully as a colorful .ans file.
    internal CellGrid? Grid { get; }
    internal bool IsColor => Grid is not null;

    public ArtProvider(IConfiguration configuration, ILogger<ArtProvider> logger)
    {
        var path = configuration["NIGHTMS_LOGIN_ART_PATH"] ?? configuration["LoginArt:Path"];

        if (!string.IsNullOrWhiteSpace(path) && File.Exists(path))
        {
            try
            {
                if (Path.GetExtension(path).Equals(".ans", StringComparison.OrdinalIgnoreCase))
                {
                    var text = File.ReadAllText(path);
                    Grid = SgrParser.Parse(text);
                    Art = DefaultArt;
                    LineCount = Grid.Height;
                    logger.LogInformation("Loaded color art from {Path} ({Width}x{Height}).", path, Grid.Width, Grid.Height);
                    return;
                }

                Art = File.ReadAllText(path).TrimEnd('\r', '\n');
                LineCount = CountLines(Art);
                logger.LogInformation("Loaded monochrome art from {Path} ({Lines} lines).", path, LineCount);
                return;
            }
            catch (Exception ex)
            {
                logger.LogWarning(ex, "Failed to read art from {Path}; falling back to default.", path);
            }
        }
        else if (!string.IsNullOrWhiteSpace(path))
        {
            logger.LogWarning("Art path {Path} is missing; using default.", path);
        }

        Art = DefaultArt;
        LineCount = CountLines(Art);
    }

    private static int CountLines(string s)
    {
        if (string.IsNullOrEmpty(s)) return 0;
        var lines = 1;
        foreach (var c in s) if (c == '\n') lines++;
        return lines;
    }
}
