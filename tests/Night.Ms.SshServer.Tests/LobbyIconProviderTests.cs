using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tests;

public class LobbyIconProviderTests : IDisposable
{
    private readonly string _dir;

    public LobbyIconProviderTests()
    {
        _dir = Path.Combine(Path.GetTempPath(), $"nightms-lobby-icons-{Guid.NewGuid():N}");
        Directory.CreateDirectory(_dir);
    }

    public void Dispose()
    {
        try { Directory.Delete(_dir, recursive: true); } catch { /* best-effort */ }
    }

    private static NightMsOptions OptionsWith(Dictionary<string, string?> values) =>
        NightMsOptions.FromConfiguration(new ConfigurationBuilder().AddInMemoryCollection(values).Build());

    private FileSystemLobbyIconProvider Build() =>
        new(
            OptionsWith(new() { ["NIGHTMS_LOBBY_ICONS_DIR"] = _dir }),
            NullLogger<FileSystemLobbyIconProvider>.Instance);

    [Fact]
    public void Get_loads_and_parses_existing_ans_file()
    {
        const string ESC = "";
        File.WriteAllText(Path.Combine(_dir, "chat.ans"), $"{ESC}[31mab{ESC}[0m\ncd");

        var grid = Build().Get("chat");

        Assert.Equal(2, grid.Width);
        Assert.Equal(2, grid.Height);
    }

    [Fact]
    public void Get_returns_placeholder_when_file_is_missing()
    {
        var grid = Build().Get("does-not-exist");

        Assert.True(grid.Width > 0);
        Assert.True(grid.Height > 0);
    }

    [Fact]
    public void Get_returns_placeholder_when_file_throws_on_read()
    {
        // Create a directory at the icon path so File.ReadAllText fails with UnauthorizedAccessException.
        Directory.CreateDirectory(Path.Combine(_dir, "broken.ans"));

        var grid = Build().Get("broken");

        Assert.True(grid.Width > 0);
        Assert.True(grid.Height > 0);
    }

    [Fact]
    public void Get_caches_result_across_calls()
    {
        File.WriteAllText(Path.Combine(_dir, "boards.ans"), "ab\ncd");
        var sut = Build();

        var first = sut.Get("boards");
        // Delete the file — a fresh load would now fall back to the placeholder. The cached
        // entry should be returned instead, proving cache behavior.
        File.Delete(Path.Combine(_dir, "boards.ans"));
        var second = sut.Get("boards");

        Assert.Same(first, second);
    }

    [Fact]
    public void Get_falls_back_to_placeholder_for_each_missing_name_independently()
    {
        var sut = Build();

        var a = sut.Get("missing-a");
        var b = sut.Get("missing-b");

        Assert.NotNull(a);
        Assert.NotNull(b);
    }

    [Fact]
    public void LobbyIcons_Path_config_key_is_an_alternative_to_the_env_var()
    {
        File.WriteAllText(Path.Combine(_dir, "profile.ans"), "x");

        var sut = new FileSystemLobbyIconProvider(
            OptionsWith(new() { ["LobbyIcons:Path"] = _dir }),
            NullLogger<FileSystemLobbyIconProvider>.Instance);

        var grid = sut.Get("profile");

        Assert.Equal(1, grid.Width);
        Assert.Equal(1, grid.Height);
    }
}
