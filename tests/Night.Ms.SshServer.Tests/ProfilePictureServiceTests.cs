using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Web;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.Formats.Png;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Tests;

public class ProfilePictureServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime, IDisposable
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private string _tempDir = string.Empty;

    public ProfilePictureServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        _tempDir = Path.Combine(Path.GetTempPath(), "pfp-tests-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(_tempDir);
    }

    public Task DisposeAsync() => Task.CompletedTask;

    public void Dispose()
    {
        try { if (Directory.Exists(_tempDir)) Directory.Delete(_tempDir, recursive: true); } catch { }
    }

    private ProfilePictureService NewSut() => new(
        new NightMsOptions { ProfilePictureDirectory = _tempDir },
        new TestDbContextFactory(_dbOptions!),
        NullLogger<ProfilePictureService>.Instance);

    private async Task<long> SeedUserAsync(string handle = "alice")
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user.Id;
    }

    private static byte[] MakePng(int width, int height)
    {
        using var img = new Image<Rgba32>(width, height, new Rgba32(0x66, 0xcc, 0x99));
        using var ms = new MemoryStream();
        img.Save(ms, new PngEncoder());
        return ms.ToArray();
    }

    [Fact]
    public async Task Save_writes_256x256_png_and_bumps_updated_at()
    {
        var userId = await SeedUserAsync();
        var sut = NewSut();
        using var src = new MemoryStream(MakePng(width: 400, height: 600));

        var ok = await sut.SaveAsync(userId, src, default);

        Assert.True(ok);
        var path = Path.Combine(_tempDir, $"{userId}.png");
        Assert.True(File.Exists(path));
        using var saved = await Image.LoadAsync<Rgba32>(path);
        Assert.Equal(256, saved.Width);
        Assert.Equal(256, saved.Height);

        await using var db = new AppDbContext(_dbOptions!);
        var user = await db.Users.FirstAsync(u => u.Id == userId);
        Assert.NotNull(user.ProfilePictureUpdatedAt);
    }

    [Fact]
    public async Task Save_rejects_non_image_payload_without_bumping_db()
    {
        var userId = await SeedUserAsync();
        var sut = NewSut();
        using var src = new MemoryStream(System.Text.Encoding.ASCII.GetBytes("totally not an image"));

        var ok = await sut.SaveAsync(userId, src, default);

        Assert.False(ok);
        Assert.False(File.Exists(Path.Combine(_tempDir, $"{userId}.png")));
        await using var db = new AppDbContext(_dbOptions!);
        var user = await db.Users.FirstAsync(u => u.Id == userId);
        Assert.Null(user.ProfilePictureUpdatedAt);
    }

    [Fact]
    public async Task Save_rejects_oversized_upload_before_decode()
    {
        var userId = await SeedUserAsync();
        var sut = NewSut();
        // 5 MB of zeroes — over the 4 MB cap. Doesn't decode; doesn't need to.
        using var src = new MemoryStream(new byte[5 * 1024 * 1024]);

        var ok = await sut.SaveAsync(userId, src, default);
        Assert.False(ok);
        Assert.False(File.Exists(Path.Combine(_tempDir, $"{userId}.png")));
    }

    [Fact]
    public async Task GetPngBytes_returns_identicon_when_no_file_present()
    {
        var userId = await SeedUserAsync(handle: "fresh");
        var sut = NewSut();

        var bytes = await sut.GetPngBytesAsync(userId, "fresh", default);

        // PNG magic bytes: 89 50 4E 47 0D 0A 1A 0A
        Assert.Equal(0x89, bytes[0]);
        Assert.Equal((byte)'P', bytes[1]);
        Assert.Equal((byte)'N', bytes[2]);
        Assert.Equal((byte)'G', bytes[3]);
        // Round-trip-decode to make sure it's a valid 256x256 PNG.
        using var img = Image.Load<Rgba32>(bytes);
        Assert.Equal(256, img.Width);
        Assert.Equal(256, img.Height);
    }

    [Fact]
    public async Task GetPngBytes_returns_uploaded_file_when_present()
    {
        var userId = await SeedUserAsync(handle: "uploaded");
        var sut = NewSut();
        // First upload so the file exists; then retrieve.
        using (var src = new MemoryStream(MakePng(width: 256, height: 256)))
        {
            Assert.True(await sut.SaveAsync(userId, src, default));
        }

        var bytes = await sut.GetPngBytesAsync(userId, "uploaded", default);
        using var img = Image.Load<Rgba32>(bytes);
        Assert.Equal(256, img.Width);
    }

    [Fact]
    public async Task Delete_removes_file_and_clears_updated_at()
    {
        var userId = await SeedUserAsync();
        var sut = NewSut();
        using (var src = new MemoryStream(MakePng(256, 256))) { await sut.SaveAsync(userId, src, default); }

        var deleted = await sut.DeleteAsync(userId, default);

        Assert.True(deleted);
        Assert.False(File.Exists(Path.Combine(_tempDir, $"{userId}.png")));
        await using var db = new AppDbContext(_dbOptions!);
        var user = await db.Users.FirstAsync(u => u.Id == userId);
        Assert.Null(user.ProfilePictureUpdatedAt);
    }
}
