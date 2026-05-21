using System.Collections.Concurrent;
using System.Text.RegularExpressions;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Tui.Art;

// Filesystem-backed gallery: reads .ans files out of a configured directory each time
// List() is called. Parsed pieces are cached by (full path, mtime, length) so a List()
// call that finds nothing changed on disk is essentially free — only new or edited files
// pay the read+parse cost. Files with malformed SGR escapes are skipped in List() (one
// warning per skip), and the failed-parse result is cached too so a broken file isn't
// re-parsed on every gallery interaction.
internal sealed class FileSystemArtGalleryProvider : IArtGalleryProvider
{
    // Strips a leading numeric ordering prefix from filenames so "010-welcome.ans" titles as
    // "welcome". Supports plain digits and digit-separator forms ("010-", "010_", "010 ").
    private static readonly Regex OrderingPrefix = new(@"^\d+[-_\s]+", RegexOptions.Compiled);

    private readonly NightMsOptions _options;
    private readonly ILogger<FileSystemArtGalleryProvider> _logger;

    // Singleton scope — shared across concurrent SSH sessions. ConcurrentDictionary handles
    // the basics; benign races (two sessions parsing the same file on a cold miss) are fine.
    private readonly ConcurrentDictionary<string, CacheEntry> _cache = new(StringComparer.Ordinal);

    public FileSystemArtGalleryProvider(NightMsOptions options, ILogger<FileSystemArtGalleryProvider> logger)
    {
        _options = options;
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
            var files = new DirectoryInfo(dir)
                .EnumerateFiles()
                .Where(f => string.Equals(f.Extension, ".ans", StringComparison.OrdinalIgnoreCase))
                .OrderBy(f => f.Name, StringComparer.OrdinalIgnoreCase)
                .ToList();

            var entries = new List<ArtGalleryEntry>(files.Count);
            var seen = new HashSet<string>(files.Count, StringComparer.Ordinal);
            foreach (var file in files)
            {
                seen.Add(file.FullName);
                var entry = ResolveCacheEntry(file);
                if (entry.Grid is null) continue;
                entries.Add(new ArtGalleryEntry(Id: file.FullName, Title: TitleFor(file.FullName)));
            }

            // Evict cache entries for files that have disappeared. A concurrent List() in
            // another session might race and re-add a key after we snapshot — worst case
            // is the other session re-parses on next access. Benign.
            foreach (var key in _cache.Keys)
            {
                if (!seen.Contains(key)) _cache.TryRemove(key, out _);
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
        if (string.IsNullOrWhiteSpace(id)) return null;
        try
        {
            var info = new FileInfo(id);
            if (!info.Exists) return null;
            return ResolveCacheEntry(info).Grid;
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to load gallery piece {Path}.", id);
            return null;
        }
    }

    // Returns the cached parse for `file` if its mtime + length match, otherwise re-reads
    // and re-parses, caching the result (including a null Grid for malformed files so we
    // don't keep retrying them).
    private CacheEntry ResolveCacheEntry(FileInfo file)
    {
        var lastWriteUtc = file.LastWriteTimeUtc;
        var length = file.Length;
        if (_cache.TryGetValue(file.FullName, out var cached)
            && cached.LastWriteTimeUtc == lastWriteUtc
            && cached.Length == length)
        {
            return cached;
        }

        CellGrid? grid;
        try
        {
            var text = File.ReadAllText(file.FullName);
            grid = SgrParser.Parse(text);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Skipping malformed gallery piece {Path}.", file.FullName);
            grid = null;
        }

        var entry = new CacheEntry(lastWriteUtc, length, grid);
        _cache[file.FullName] = entry;
        return entry;
    }

    private string? ResolveDirectory() => _options.ArtGalleryPath ?? DefaultDirectory();

    private static string DefaultDirectory() => Path.Combine(AppContext.BaseDirectory, "art", "gallery");

    private static string TitleFor(string path)
    {
        var name = Path.GetFileNameWithoutExtension(path);
        return OrderingPrefix.Replace(name, string.Empty);
    }

    private sealed record CacheEntry(DateTime LastWriteTimeUtc, long Length, CellGrid? Grid);
}
