using System.Text;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Tui.Art;

// Loads small ANSI-art pictographs for the lobby carousel cards. One <name>.ans per button
// dropped in NIGHTMS_LOBBY_ICONS_DIR (default {AppContext.BaseDirectory}/art/lobby-icons/),
// resolved by name on first request and cached. Missing or malformed files fall back to a
// built-in '?' placeholder so the carousel always has something to draw.
internal interface ILobbyIconProvider
{
    CellGrid Get(string name);
}

internal sealed class FileSystemLobbyIconProvider : ILobbyIconProvider
{
    private readonly NightMsOptions _options;
    private readonly ILogger<FileSystemLobbyIconProvider> _logger;
    private readonly Dictionary<string, CellGrid> _cache = new(StringComparer.OrdinalIgnoreCase);
    private readonly HashSet<string> _warned = new(StringComparer.OrdinalIgnoreCase);
    private readonly Lock _lock = new();

    public FileSystemLobbyIconProvider(NightMsOptions options, ILogger<FileSystemLobbyIconProvider> logger)
    {
        _options = options;
        _logger = logger;
    }

    public CellGrid Get(string name)
    {
        lock (_lock)
        {
            if (_cache.TryGetValue(name, out var hit)) return hit;
            var grid = Load(name) ?? Placeholder();
            _cache[name] = grid;
            return grid;
        }
    }

    private CellGrid? Load(string name)
    {
        var dir = _options.LobbyIconsPath ?? Path.Combine(AppContext.BaseDirectory, "art", "lobby-icons");
        var path = Path.Combine(dir, name + ".ans");
        if (!File.Exists(path))
        {
            WarnOnce(name, $"Lobby icon {name}.ans not found at {path}; using placeholder.");
            return null;
        }
        try
        {
            return SgrParser.Parse(File.ReadAllText(path));
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to parse lobby icon {Path}; using placeholder.", path);
            return null;
        }
    }

    private void WarnOnce(string name, string message)
    {
        if (_warned.Add(name)) _logger.LogInformation(message);
    }

    // 10×2 framed '?' rendered in the BBS accent palette. Width matches the inner content area
    // of an unselected card (12 cols) with a one-col gutter on each side; on a selected card it
    // simply sits left-of-center which the carousel painter accepts.
    private static CellGrid Placeholder()
    {
        var grid = new CellGrid(10, 2);
        var fg = new ArtColor(85, 255, 255); // BrightCyan-ish — matches BbsTheme.Accent
        var bg = ArtColor.Black;
        for (var x = 0; x < grid.Width; x++)
        {
            grid[x, 0] = new Cell(new Rune('░'), fg, bg, ArtStyle.None);
            grid[x, 1] = new Cell(new Rune('░'), fg, bg, ArtStyle.None);
        }
        grid[4, 0] = new Cell(new Rune('?'), fg, bg, ArtStyle.Bold);
        grid[5, 0] = new Cell(new Rune(' '), fg, bg, ArtStyle.None);
        return grid;
    }
}
