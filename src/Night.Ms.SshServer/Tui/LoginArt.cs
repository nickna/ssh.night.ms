namespace Night.Ms.SshServer.Tui;

// Provides the ASCII banner shown at the top of RegisterScreen + LobbyScreen. Reads from
// NIGHTMS_LOGIN_ART_PATH (or the LoginArt:Path config key) if set; falls back to a small
// inline default. Intentionally monochrome — Terminal.Gui v2 Labels don't interpret raw
// ANSI escapes inside their Text, so per-character color would need a custom view.
public sealed class LoginArtProvider
{
    public const string DefaultArt =
        " ╓──────────────────────────────────────────────────╖\n" +
        " ║                                                  ║\n" +
        " ║   ssh.night.ms   ▒▓█  a small bbs over ssh  █▓▒  ║\n" +
        " ║                                                  ║\n" +
        " ╙──────────────────────────────────────────────────╜";

    public string Art { get; }
    public int LineCount { get; }

    public LoginArtProvider(IConfiguration configuration, ILogger<LoginArtProvider> logger)
    {
        var path = configuration["NIGHTMS_LOGIN_ART_PATH"] ?? configuration["LoginArt:Path"];
        if (!string.IsNullOrWhiteSpace(path) && File.Exists(path))
        {
            try
            {
                Art = File.ReadAllText(path).TrimEnd('\r', '\n');
                logger.LogInformation("Loaded login art from {Path} ({Lines} lines).", path, CountLines(Art));
            }
            catch (Exception ex)
            {
                logger.LogWarning(ex, "Failed to read login art from {Path}; falling back to default.", path);
                Art = DefaultArt;
            }
        }
        else
        {
            if (!string.IsNullOrWhiteSpace(path))
            {
                logger.LogWarning("NIGHTMS_LOGIN_ART_PATH points to {Path} but the file is missing; using default.", path);
            }
            Art = DefaultArt;
        }
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
