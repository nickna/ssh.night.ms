using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tests;

public class FileSystemArtGalleryProviderTests : IDisposable
{
    private readonly string _dir;

    public FileSystemArtGalleryProviderTests()
    {
        _dir = Path.Combine(Path.GetTempPath(), $"nightms-gallery-{Guid.NewGuid():N}");
        Directory.CreateDirectory(_dir);
    }

    public void Dispose()
    {
        try { Directory.Delete(_dir, recursive: true); } catch { /* best-effort */ }
    }

    private static NightMsOptions OptionsWith(Dictionary<string, string?> values) =>
        NightMsOptions.FromConfiguration(new ConfigurationBuilder().AddInMemoryCollection(values).Build());

    private FileSystemArtGalleryProvider Build() =>
        new(
            OptionsWith(new Dictionary<string, string?> { ["NIGHTMS_ART_DIR"] = _dir }),
            NullLogger<FileSystemArtGalleryProvider>.Instance);

    [Fact]
    public void Returns_empty_when_directory_is_missing()
    {
        var sut = new FileSystemArtGalleryProvider(
            OptionsWith(new Dictionary<string, string?> { ["NIGHTMS_ART_DIR"] = Path.Combine(_dir, "does-not-exist") }),
            NullLogger<FileSystemArtGalleryProvider>.Instance);

        Assert.Empty(sut.List());
    }

    [Fact]
    public void Returns_empty_when_directory_is_empty()
    {
        Assert.Empty(Build().List());
    }

    [Fact]
    public void Lists_ans_files_in_alphabetical_order()
    {
        File.WriteAllText(Path.Combine(_dir, "020-second.ans"), "x");
        File.WriteAllText(Path.Combine(_dir, "010-first.ans"), "x");
        File.WriteAllText(Path.Combine(_dir, "030-third.ans"), "x");

        var entries = Build().List();

        Assert.Equal(3, entries.Count);
        Assert.Equal("first", entries[0].Title);
        Assert.Equal("second", entries[1].Title);
        Assert.Equal("third", entries[2].Title);
    }

    [Fact]
    public void Ignores_non_ans_files()
    {
        File.WriteAllText(Path.Combine(_dir, "010-keep.ans"), "x");
        File.WriteAllText(Path.Combine(_dir, "skip.txt"), "x");
        File.WriteAllText(Path.Combine(_dir, "skip.png"), "x");
        File.WriteAllText(Path.Combine(_dir, "readme"), "x");

        var entries = Build().List();

        Assert.Single(entries);
        Assert.Equal("keep", entries[0].Title);
    }

    [Fact]
    public void Ans_extension_match_is_case_insensitive()
    {
        File.WriteAllText(Path.Combine(_dir, "010-upper.ANS"), "x");
        File.WriteAllText(Path.Combine(_dir, "020-mixed.AnS"), "x");

        var entries = Build().List();

        Assert.Equal(2, entries.Count);
    }

    [Fact]
    public void Title_strips_numeric_prefix_and_separator()
    {
        File.WriteAllText(Path.Combine(_dir, "010-hyphen.ans"), "x");
        File.WriteAllText(Path.Combine(_dir, "020_underscore.ans"), "x");
        File.WriteAllText(Path.Combine(_dir, "030 space.ans"), "x");
        File.WriteAllText(Path.Combine(_dir, "no-prefix.ans"), "x");

        var titles = Build().List().Select(e => e.Title).ToList();

        Assert.Contains("hyphen", titles);
        Assert.Contains("underscore", titles);
        Assert.Contains("space", titles);
        Assert.Contains("no-prefix", titles);
    }

    [Fact]
    public void Id_is_the_full_path_so_Load_can_round_trip()
    {
        var path = Path.Combine(_dir, "010-art.ans");
        File.WriteAllText(path, "ab\ncd");

        var entry = Build().List().Single();

        Assert.Equal(path, entry.Id);

        var grid = Build().Load(entry.Id);
        Assert.NotNull(grid);
        Assert.Equal(2, grid!.Width);
        Assert.Equal(2, grid.Height);
    }

    [Fact]
    public void Load_returns_null_for_missing_id()
    {
        Assert.Null(Build().Load(Path.Combine(_dir, "nothing.ans")));
    }

    [Fact]
    public void Load_reflects_file_edits_via_cache_invalidation()
    {
        // Provider caches parsed pieces by (mtime, length). Editing the file (here, by
        // changing its length) must invalidate the cache so a subsequent Load sees the new
        // content, not the stale parse.
        var path = Path.Combine(_dir, "010-art.ans");
        File.WriteAllText(path, "ab\ncd"); // 2x2

        var sut = Build();
        var first = sut.Load(sut.List().Single().Id);
        Assert.NotNull(first);
        Assert.Equal(2, first!.Width);

        File.WriteAllText(path, "abcd\nefgh\nijkl"); // 4x3 — different length forces re-parse

        var second = sut.Load(sut.List().Single().Id);
        Assert.NotNull(second);
        Assert.Equal(4, second!.Width);
        Assert.Equal(3, second.Height);
    }

    [Fact]
    public void List_drops_deleted_files_from_cache()
    {
        var path = Path.Combine(_dir, "010-art.ans");
        File.WriteAllText(path, "ab\ncd");

        var sut = Build();
        Assert.Single(sut.List());

        File.Delete(path);
        Assert.Empty(sut.List());
        // After the eviction pass, Load on the stale id must not resurrect the entry.
        Assert.Null(sut.Load(path));
    }

    [Fact]
    public void ArtGallery_Path_config_key_is_an_alternative_to_the_env_var()
    {
        File.WriteAllText(Path.Combine(_dir, "010-art.ans"), "x");

        var sut = new FileSystemArtGalleryProvider(
            OptionsWith(new Dictionary<string, string?> { ["ArtGallery:Path"] = _dir }),
            NullLogger<FileSystemArtGalleryProvider>.Instance);

        Assert.Single(sut.List());
    }
}
