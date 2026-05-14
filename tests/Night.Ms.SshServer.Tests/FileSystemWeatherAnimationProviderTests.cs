using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Providers;
using Night.Ms.SshServer.Tui.Art;

namespace Night.Ms.SshServer.Tests;

public class FileSystemWeatherAnimationProviderTests : IDisposable
{
    private readonly string _dir;

    public FileSystemWeatherAnimationProviderTests()
    {
        _dir = Path.Combine(Path.GetTempPath(), $"nightms-weather-art-{Guid.NewGuid():N}");
        Directory.CreateDirectory(_dir);
    }

    public void Dispose()
    {
        try { Directory.Delete(_dir, recursive: true); } catch { /* best-effort */ }
    }

    private FileSystemWeatherAnimationProvider Build()
    {
        var cfg = new ConfigurationBuilder()
            .AddInMemoryCollection(new Dictionary<string, string?> { ["NIGHTMS_WEATHER_ART_DIR"] = _dir })
            .Build();
        return new FileSystemWeatherAnimationProvider(
            NightMsOptions.FromConfiguration(cfg),
            NullLogger<FileSystemWeatherAnimationProvider>.Instance);
    }

    private void WriteFrame(string slug, string filename, string content)
    {
        var conditionDir = Path.Combine(_dir, slug);
        Directory.CreateDirectory(conditionDir);
        File.WriteAllText(Path.Combine(conditionDir, filename), content);
    }

    [Fact]
    public void Returns_frames_in_alphabetical_order()
    {
        WriteFrame("rain", "frame-02.ans", "BB");
        WriteFrame("rain", "frame-01.ans", "A");
        WriteFrame("rain", "frame-03.ans", "CCC");

        var frames = Build().GetFrames(WeatherCondition.Rain);

        Assert.Equal(3, frames.Count);
        Assert.Equal(1, frames[0].Width);
        Assert.Equal(2, frames[1].Width);
        Assert.Equal(3, frames[2].Width);
    }

    [Fact]
    public void Falls_back_to_default_slug_when_condition_directory_is_missing()
    {
        WriteFrame("default", "frame-01.ans", "fallback");

        var frames = Build().GetFrames(WeatherCondition.Thunderstorm);

        Assert.Single(frames);
        Assert.Equal(8, frames[0].Width); // "fallback".Length
    }

    [Fact]
    public void Returns_empty_when_condition_and_default_are_both_missing()
    {
        var frames = Build().GetFrames(WeatherCondition.Snow);
        Assert.Empty(frames);
    }

    [Fact]
    public void Ignores_non_ans_files()
    {
        WriteFrame("rain", "frame-01.ans", "ok");
        WriteFrame("rain", "frame-02.txt", "nope");
        WriteFrame("rain", "readme", "nope");

        var frames = Build().GetFrames(WeatherCondition.Rain);

        Assert.Single(frames);
    }

    [Fact]
    public void Caches_frames_so_repeat_reads_share_one_disk_pass()
    {
        WriteFrame("rain", "frame-01.ans", "first");

        var sut = Build();
        var first = sut.GetFrames(WeatherCondition.Rain);

        // Mutate disk after the first read — the cached result should not pick up the change.
        File.WriteAllText(Path.Combine(_dir, "rain", "frame-02.ans"), "second");
        var second = sut.GetFrames(WeatherCondition.Rain);

        Assert.Same(first, second);
        Assert.Single(second);
    }

    [Fact]
    public void Returns_empty_when_root_directory_is_missing()
    {
        var cfg = new ConfigurationBuilder()
            .AddInMemoryCollection(new Dictionary<string, string?> { ["NIGHTMS_WEATHER_ART_DIR"] = Path.Combine(_dir, "does-not-exist") })
            .Build();
        var sut = new FileSystemWeatherAnimationProvider(
            NightMsOptions.FromConfiguration(cfg),
            NullLogger<FileSystemWeatherAnimationProvider>.Instance);

        Assert.Empty(sut.GetFrames(WeatherCondition.Rain));
    }
}
