using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
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

    private FileSystemArtGalleryProvider Build() =>
        new(
            new ConfigurationBuilder()
                .AddInMemoryCollection(new Dictionary<string, string?> { ["NIGHTMS_ART_DIR"] = _dir })
                .Build(),
            NullLogger<FileSystemArtGalleryProvider>.Instance);

    [Fact]
    public void Returns_empty_when_directory_is_missing()
    {
        var sut = new FileSystemArtGalleryProvider(
            new ConfigurationBuilder()
                .AddInMemoryCollection(new Dictionary<string, string?> { ["NIGHTMS_ART_DIR"] = Path.Combine(_dir, "does-not-exist") })
                .Build(),
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
    public void ArtGallery_Path_config_key_is_an_alternative_to_the_env_var()
    {
        File.WriteAllText(Path.Combine(_dir, "010-art.ans"), "x");

        var sut = new FileSystemArtGalleryProvider(
            new ConfigurationBuilder()
                .AddInMemoryCollection(new Dictionary<string, string?> { ["ArtGallery:Path"] = _dir })
                .Build(),
            NullLogger<FileSystemArtGalleryProvider>.Instance);

        Assert.Single(sut.List());
    }
}
