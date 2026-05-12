using System.Text.RegularExpressions;

namespace Night.Ms.SshServer.Tui.Art;

// Filesystem-backed gallery: reads .ans files out of a configured directory each time
// List() is called. Cheap enough for small collections (a few dozen pieces); add an LRU
// cache if a real library outgrows that. Files with malformed SGR escapes are skipped in
// List() so the screen never sees a broken entry; one warning is logged per skipped file.
internal sealed class FileSystemArtGalleryProvider : IArtGalleryProvider
{
    // Strips a leading numeric ordering prefix from filenames so "010-welcome.ans" titles as
    // "welcome". Supports plain digits and digit-separator forms ("010-", "010_", "010 ").
    private static readonly Regex OrderingPrefix = new(@"^\d+[-_\s]+", RegexOptions.Compiled);

    private readonly IConfiguration _configuration;
    private readonly ILogger<FileSystemArtGalleryProvider> _logger;

    public FileSystemArtGalleryProvider(IConfiguration configuration, ILogger<FileSystemArtGalleryProvider> logger)
    {
        _configuration = configuration;
        _logger = logger;
    }

    public IReadOnlyList<ArtGalleryEntry> List()
    {
        var dir = ResolveDirectory();
        if (dir is null || !Directory.Exists(dir))
        {
            return [];
        }

        try
        {
            var files = Directory.EnumerateFiles(dir)
                .Where(p => string.Equals(Path.GetExtension(p), ".ans", StringComparison.OrdinalIgnoreCase))
                .OrderBy(p => Path.GetFileName(p), StringComparer.OrdinalIgnoreCase)
                .ToList();

            var entries = new List<ArtGalleryEntry>(files.Count);
            foreach (var path in files)
            {
                if (!ValidatesAsArt(path)) continue;
                entries.Add(new ArtGalleryEntry(Id: path, Title: TitleFor(path)));
            }
            return entries;
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to enumerate gallery directory {Path}.", dir);
            return [];
        }
    }

    public CellGrid? Load(string id)
    {
        if (string.IsNullOrWhiteSpace(id) || !File.Exists(id)) return null;
        try
        {
            var text = File.ReadAllText(id);
            return SgrParser.Parse(text);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to load gallery piece {Path}.", id);
            return null;
        }
    }

    // Parse once at list time so a malformed file never reaches the screen.
    private bool ValidatesAsArt(string path)
    {
        try
        {
            var text = File.ReadAllText(path);
            _ = SgrParser.Parse(text);
            return true;
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Skipping malformed gallery piece {Path}.", path);
            return false;
        }
    }

    private string? ResolveDirectory() =>
        _configuration["NIGHTMS_ART_DIR"]
        ?? _configuration["ArtGallery:Path"]
        ?? DefaultDirectory();

    private static string DefaultDirectory() => Path.Combine(AppContext.BaseDirectory, "art", "gallery");

    private static string TitleFor(string path)
    {
        var name = Path.GetFileNameWithoutExtension(path);
        return OrderingPrefix.Replace(name, string.Empty);
    }
}
