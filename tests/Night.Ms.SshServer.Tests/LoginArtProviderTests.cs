using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Tui;

namespace Night.Ms.SshServer.Tests;

public class LoginArtProviderTests
{
    private static IConfiguration ConfigWith(Dictionary<string, string?>? values = null) =>
        new ConfigurationBuilder().AddInMemoryCollection(values ?? new Dictionary<string, string?>()).Build();

    [Fact]
    public void Returns_the_built_in_default_when_no_path_is_configured()
    {
        var sut = new LoginArtProvider(ConfigWith(), NullLogger<LoginArtProvider>.Instance);

        Assert.Equal(LoginArtProvider.DefaultArt, sut.Art);
        Assert.Equal(5, sut.LineCount); // default art is 5 lines
    }

    [Fact]
    public void Returns_the_built_in_default_when_configured_path_does_not_exist()
    {
        var missing = Path.Combine(Path.GetTempPath(), $"nightms-art-missing-{Guid.NewGuid():N}.txt");
        var sut = new LoginArtProvider(
            ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = missing }),
            NullLogger<LoginArtProvider>.Instance);

        Assert.Equal(LoginArtProvider.DefaultArt, sut.Art);
    }

    [Fact]
    public void Loads_art_from_a_file_when_the_path_exists()
    {
        var tmp = Path.GetTempFileName();
        try
        {
            File.WriteAllText(tmp, "line-one\nline-two\nline-three\n");
            var sut = new LoginArtProvider(
                ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = tmp }),
                NullLogger<LoginArtProvider>.Instance);

            Assert.Equal("line-one\nline-two\nline-three", sut.Art); // trailing \n stripped
            Assert.Equal(3, sut.LineCount);
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* best-effort */ }
        }
    }

    [Fact]
    public void Falls_back_to_default_when_the_file_read_throws()
    {
        // Pointing at a directory rather than a file makes File.ReadAllText throw.
        var sut = new LoginArtProvider(
            ConfigWith(new() { ["NIGHTMS_LOGIN_ART_PATH"] = Path.GetTempPath() }),
            NullLogger<LoginArtProvider>.Instance);

        // Path exists (it's a directory) so the existence check passes; the read then fails.
        // Either way we end up with the default — graceful degradation.
        Assert.Equal(LoginArtProvider.DefaultArt, sut.Art);
    }

    [Fact]
    public void LoginArt_Path_config_key_is_an_alternative_to_the_env_var()
    {
        var tmp = Path.GetTempFileName();
        try
        {
            File.WriteAllText(tmp, "configured\nvia\nappsettings");
            var sut = new LoginArtProvider(
                ConfigWith(new() { ["LoginArt:Path"] = tmp }),
                NullLogger<LoginArtProvider>.Instance);

            Assert.Equal("configured\nvia\nappsettings", sut.Art);
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* best-effort */ }
        }
    }
}
