using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Tui;

namespace Night.Ms.SshServer.Tests;

public class ArtProviderTests
{
    private static IConfiguration ConfigWith(Dictionary<string, string?>? values = null) =>
        new ConfigurationBuilder().AddInMemoryCollection(values ?? new Dictionary<string, string?>()).Build();

    [Fact]
    public void Returns_the_built_in_default_when_no_path_is_configured()
    {
        var sut = new ArtProvider(ConfigWith(), NullLogger<ArtProvider>.Instance);

        Assert.Equal(ArtProvider.DefaultArt, sut.Art);
        Assert.Equal(5, sut.LineCount);
        Assert.False(sut.IsColor);
    }

    [Fact]
    public void Returns_the_built_in_default_when_configured_path_does_not_exist()
    {
        var missing = Path.Combine(Path.GetTempPath(), $"nightms-art-missing-{Guid.NewGuid():N}.txt");
        var sut = new ArtProvider(
            ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = missing }),
            NullLogger<ArtProvider>.Instance);

        Assert.Equal(ArtProvider.DefaultArt, sut.Art);
        Assert.False(sut.IsColor);
    }

    [Fact]
    public void Loads_art_from_a_text_file_when_the_path_exists()
    {
        var tmp = Path.GetTempFileName();
        try
        {
            File.WriteAllText(tmp, "line-one\nline-two\nline-three\n");
            var sut = new ArtProvider(
                ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = tmp }),
                NullLogger<ArtProvider>.Instance);

            Assert.Equal("line-one\nline-two\nline-three", sut.Art);
            Assert.Equal(3, sut.LineCount);
            Assert.False(sut.IsColor);
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* best-effort */ }
        }
    }

    [Fact]
    public void Falls_back_to_default_when_the_file_read_throws()
    {
        var sut = new ArtProvider(
            ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = Path.GetTempPath() }),
            NullLogger<ArtProvider>.Instance);

        Assert.Equal(ArtProvider.DefaultArt, sut.Art);
        Assert.False(sut.IsColor);
    }

    [Fact]
    public void LoginArt_Path_config_key_is_an_alternative_to_the_env_var()
    {
        var tmp = Path.GetTempFileName();
        try
        {
            File.WriteAllText(tmp, "configured\nvia\nappsettings");
            var sut = new ArtProvider(
                ConfigWith(new() { ["LoginArt:Path"] = tmp }),
                NullLogger<ArtProvider>.Instance);

            Assert.Equal("configured\nvia\nappsettings", sut.Art);
            Assert.False(sut.IsColor);
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* best-effort */ }
        }
    }

    [Fact]
    public void Loads_ans_file_as_color_grid()
    {
        var tmp = Path.ChangeExtension(Path.GetTempFileName(), ".ans");
        try
        {
            const string ESC = "";
            File.WriteAllText(tmp, $"{ESC}[31ma{ESC}[0mb\ncd");
            var sut = new ArtProvider(
                ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = tmp }),
                NullLogger<ArtProvider>.Instance);

            Assert.True(sut.IsColor);
            Assert.NotNull(sut.Grid);
            Assert.Equal(2, sut.Grid!.Width);
            Assert.Equal(2, sut.Grid.Height);
            Assert.Equal(2, sut.LineCount);
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* best-effort */ }
        }
    }

    [Fact]
    public void Ans_extension_match_is_case_insensitive()
    {
        var tmp = Path.ChangeExtension(Path.GetTempFileName(), ".ANS");
        try
        {
            File.WriteAllText(tmp, "x");
            var sut = new ArtProvider(
                ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = tmp }),
                NullLogger<ArtProvider>.Instance);

            Assert.True(sut.IsColor);
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* best-effort */ }
        }
    }
}
