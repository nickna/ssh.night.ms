using Microsoft.EntityFrameworkCore;
using Night.Ms.Imaging;
using Night.Ms.SshServer.Caching;
using Night.Ms.SshServer.Configuration;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui.Art;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.Formats;
using SixLabors.ImageSharp.Formats.Png;
using SixLabors.ImageSharp.PixelFormats;
using SixLabors.ImageSharp.Processing;

namespace Night.Ms.SshServer.Web;

// Single entry point for everything profile-picture: upload validation + storage, on-disk
// PNG lookup, identicon fallback, and TUI CellGrid rendering. Storage is flat-file under
// the directory resolved from NIGHTMS_PFP_DIR (or {AppContext.BaseDirectory}/data/pfp by
// default). One PNG per user at {dir}/{user_id}.png, 256x256 canonical, written exactly
// when User.ProfilePictureUpdatedAt is non-null.
internal sealed class ProfilePictureService
{
    public const long MaxUploadBytes = 4 * 1024 * 1024;
    public const int MaxSourceDimension = 4096;
    public const int CanonicalSize = 256;

    private static readonly DecoderOptions DecoderOpts = new() { MaxFrames = 1 };
    private static readonly PngEncoder PngEnc = new();

    // CellGrid cache: keyed on (handle, cols, updated_at-ticks-or-zero). 256 entries is plenty
    // for a single-server BBS — a single user with many sessions still hits the same key.
    private readonly FifoAsyncCache<string, CellGrid> _gridCache = new(maxEntries: 256);

    private readonly NightMsOptions _options;
    private readonly IDbContextFactory<AppDbContext> _dbFactory;
    private readonly ILogger<ProfilePictureService> _log;

    public ProfilePictureService(NightMsOptions options, IDbContextFactory<AppDbContext> dbFactory, ILogger<ProfilePictureService> log)
    {
        _options = options;
        _dbFactory = dbFactory;
        _log = log;
        Directory.CreateDirectory(ResolveDirectory());
    }

    public string ResolveDirectory() =>
        _options.ProfilePictureDirectory
        ?? Path.Combine(AppContext.BaseDirectory, "data", "pfp");

    private string PathFor(long userId) => Path.Combine(ResolveDirectory(), $"{userId}.png");

    // SaveAsync: validates + center-crops + resizes + writes the canonical PNG, and updates
    // User.ProfilePictureUpdatedAt in the same transaction. Returns false (without touching
    // the DB) if the source can't be decoded, is too big, or has out-of-range dimensions.
    public async Task<bool> SaveAsync(long userId, Stream source, CancellationToken cancellationToken)
    {
        // Buffer up to MaxUploadBytes + 1 so we can detect overflow. Identify before decode
        // so a 4096x4096 PNG (well-formed but too big) doesn't get fully decoded.
        using var ms = new MemoryStream();
        var buffer = new byte[64 * 1024];
        int read;
        while ((read = await source.ReadAsync(buffer, cancellationToken).ConfigureAwait(false)) > 0)
        {
            if (ms.Length + read > MaxUploadBytes)
            {
                _log.LogInformation("PFP upload rejected for user={UserId}: exceeded {Max} bytes", userId, MaxUploadBytes);
                return false;
            }
            ms.Write(buffer, 0, read);
        }
        ms.Position = 0;

        ImageInfo? info;
        try { info = await Image.IdentifyAsync(DecoderOpts, ms, cancellationToken).ConfigureAwait(false); }
        catch (Exception ex) { _log.LogInformation(ex, "PFP upload rejected for user={UserId}: not a recognized image", userId); return false; }
        if (info is null) return false;
        if (info.Width > MaxSourceDimension || info.Height > MaxSourceDimension)
        {
            _log.LogInformation("PFP upload rejected for user={UserId}: dimensions {W}x{H} exceed {Max}",
                userId, info.Width, info.Height, MaxSourceDimension);
            return false;
        }

        ms.Position = 0;
        using var image = await Image.LoadAsync<Rgba32>(DecoderOpts, ms, cancellationToken).ConfigureAwait(false);

        // Center-crop the long side, then resize to canonical 256x256. The Bicubic sampler
        // gives sharper avatars than Lanczos at this scale (Lanczos can introduce ringing on
        // photos already at low res).
        var side = Math.Min(image.Width, image.Height);
        var cropX = (image.Width - side) / 2;
        var cropY = (image.Height - side) / 2;
        image.Mutate(ctx => ctx
            .Crop(new Rectangle(cropX, cropY, side, side))
            .Resize(new ResizeOptions
            {
                Size = new Size(CanonicalSize, CanonicalSize),
                Sampler = KnownResamplers.Bicubic,
                Mode = ResizeMode.Stretch,
            }));

        var path = PathFor(userId);
        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
        await image.SaveAsync(path, PngEnc, cancellationToken).ConfigureAwait(false);

        var now = DateTimeOffset.UtcNow;
        await using (var db = await _dbFactory.CreateDbContextAsync(cancellationToken))
        {
            var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId, cancellationToken);
            if (user is null)
            {
                _log.LogWarning("PFP save: user id={UserId} not found post-save", userId);
                return false;
            }
            user.ProfilePictureUpdatedAt = now;
            await db.SaveChangesAsync(cancellationToken);
        }
        return true;
    }

    public async Task<bool> DeleteAsync(long userId, CancellationToken cancellationToken)
    {
        var path = PathFor(userId);
        try { if (File.Exists(path)) File.Delete(path); }
        catch (Exception ex) { _log.LogWarning(ex, "PFP delete: removing file failed for user={UserId}", userId); }

        await using var db = await _dbFactory.CreateDbContextAsync(cancellationToken);
        var user = await db.Users.FirstOrDefaultAsync(u => u.Id == userId, cancellationToken);
        if (user is null) return false;
        user.ProfilePictureUpdatedAt = null;
        await db.SaveChangesAsync(cancellationToken);
        return true;
    }

    // Returns the path to the user's stored PFP if it exists, or null. Web layer streams this
    // directly via Results.File; no need to copy through the service.
    public string? GetPngPathOrNull(long userId)
    {
        var path = PathFor(userId);
        return File.Exists(path) ? path : null;
    }

    // Returns PNG bytes — either the uploaded file or a freshly-generated identicon. ETag
    // basis is the user's ProfilePictureUpdatedAt (ticks) or a stable "identicon-{handle}"
    // marker the caller can format. Avatar served via this path is always 256x256 PNG.
    public async Task<byte[]> GetPngBytesAsync(long userId, string handle, CancellationToken cancellationToken)
    {
        var path = PathFor(userId);
        if (File.Exists(path))
        {
            return await File.ReadAllBytesAsync(path, cancellationToken).ConfigureAwait(false);
        }
        using var identicon = IdenticonRenderer.Generate(handle, CanonicalSize);
        using var outMs = new MemoryStream();
        await identicon.SaveAsync(outMs, PngEnc, cancellationToken).ConfigureAwait(false);
        return outMs.ToArray();
    }

    // Returns a CellGrid suitable for direct rendering by AnsiArtView. Width is `cols`,
    // height comes out to about cols/2 (square source over half-block cells = 2:1 aspect).
    // Cached in-process keyed on (handle, cols, updated_at) so multiple TUI sessions don't
    // each re-render.
    public Task<CellGrid> GetCellGridAsync(long userId, string handle, int cols, DateTimeOffset? updatedAt, CancellationToken cancellationToken)
    {
        ArgumentOutOfRangeException.ThrowIfLessThan(cols, 8);
        var key = $"{handle}\x1f{cols}\x1f{updatedAt?.UtcTicks ?? 0}";
        return _gridCache.GetOrAddAsync(key, _ => BuildCellGridAsync(userId, handle, cols, cancellationToken), cancellationToken);
    }

    private async Task<CellGrid> BuildCellGridAsync(long userId, string handle, int cols, CancellationToken cancellationToken)
    {
        Image<Rgba32> image;
        var path = PathFor(userId);
        if (File.Exists(path))
        {
            image = await Image.LoadAsync<Rgba32>(DecoderOpts, path, cancellationToken).ConfigureAwait(false);
        }
        else
        {
            image = IdenticonRenderer.Generate(handle, CanonicalSize);
        }
        try
        {
            var ansi = HalfBlockRenderer.Render(image, cols, ColorDepth.Truecolor, DitherMode.None);
            return SgrParser.Parse(ansi);
        }
        finally
        {
            image.Dispose();
        }
    }
}
