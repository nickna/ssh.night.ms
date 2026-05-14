using System.Collections.Concurrent;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Providers;

namespace Night.Ms.SshServer.Tui.Art;

// Reads weather-banner frames from disk under {WeatherArtPath}/{condition-slug}/. Frame
// files are *.ans, sorted alphabetically — so frame-01.ans, frame-02.ans, ... yields the
// expected loop order. Malformed frames are skipped (one warning each) so the screen
// never sees a broken CellGrid.
//
// First call per condition does the I/O and parse; subsequent calls return the cached
// CellGrid[]. The 500ms frame pump in WeatherScreen therefore costs zero allocations and
// zero disk hits — the only cost is the per-frame draw.
//
// Conditions with no frames fall back to the "default" slug. If "default" is also empty,
// returns an empty list and the screen hides the banner area.
internal sealed class FileSystemWeatherAnimationProvider : IWeatherAnimationProvider
{
    private const string DefaultSlug = "default";

    private readonly NightMsOptions _options;
    private readonly ILogger<FileSystemWeatherAnimationProvider> _logger;
    private readonly ConcurrentDictionary<string, IReadOnlyList<CellGrid>> _cache = new(StringComparer.OrdinalIgnoreCase);

    public FileSystemWeatherAnimationProvider(NightMsOptions options, ILogger<FileSystemWeatherAnimationProvider> logger)
    {
        _options = options;
        _logger = logger;
    }

    public IReadOnlyList<CellGrid> GetFrames(WeatherCondition condition)
    {
        var slug = condition.ToSlug();
        var frames = LoadForSlug(slug);
        if (frames.Count > 0 || string.Equals(slug, DefaultSlug, StringComparison.OrdinalIgnoreCase))
        {
            return frames;
        }
        return LoadForSlug(DefaultSlug);
    }

    private IReadOnlyList<CellGrid> LoadForSlug(string slug) =>
        _cache.GetOrAdd(slug, ReadFromDisk);

    private IReadOnlyList<CellGrid> ReadFromDisk(string slug)
    {
        var root = ResolveRoot();
        if (root is null || !Directory.Exists(root)) return Array.Empty<CellGrid>();

        var dir = Path.Combine(root, slug);
        if (!Directory.Exists(dir)) return Array.Empty<CellGrid>();

        try
        {
            var files = Directory.EnumerateFiles(dir)
                .Where(p => string.Equals(Path.GetExtension(p), ".ans", StringComparison.OrdinalIgnoreCase))
                .OrderBy(p => Path.GetFileName(p), StringComparer.OrdinalIgnoreCase)
                .ToList();

            var frames = new List<CellGrid>(files.Count);
            foreach (var path in files)
            {
                try
                {
                    var text = File.ReadAllText(path);
                    frames.Add(SgrParser.Parse(text));
                }
                catch (Exception ex)
                {
                    _logger.LogWarning(ex, "Skipping malformed weather frame {Path}.", path);
                }
            }
            return frames;
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to enumerate weather frames in {Path}.", dir);
            return Array.Empty<CellGrid>();
        }
    }

    private string? ResolveRoot() => _options.WeatherArtPath ?? DefaultRoot();

    private static string DefaultRoot() => Path.Combine(AppContext.BaseDirectory, "art", "weather");
}
