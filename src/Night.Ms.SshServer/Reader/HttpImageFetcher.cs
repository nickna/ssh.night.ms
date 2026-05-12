using System.Collections.Concurrent;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.Formats;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Reader;

// HTTP-backed IImageFetcher with size cap, content-type guard, decode-time dimension cap,
// and a small in-process LRU-ish cache. Singleton so the cache and HttpClient pool are
// shared across all sessions; ImageSharp 3.x has a known decode-time DoS CVE so the size
// + dimension caps + per-request timeout are load-bearing for safety, not just polish.
//
// Eviction: FIFO over a max-entries cap. We don't Dispose() evicted images — Image<Rgba32>
// uses pooled buffers but managed-GC handles eventual reclamation; explicit disposal would
// race against any in-flight render that's already pulled the image from the cache.
public sealed class HttpImageFetcher(
    IHttpClientFactory httpClientFactory,
    ILogger<HttpImageFetcher> logger)
    : IImageFetcher
{
    public const string HttpClientName = "image-fetcher";

    private const int MaxBytes = 2 * 1024 * 1024;
    private const int MaxDimension = 4096;
    private const int MaxCacheEntries = 64;
    private static readonly TimeSpan FetchTimeout = TimeSpan.FromSeconds(5);

    private readonly ConcurrentDictionary<Uri, Lazy<Task<Image<Rgba32>?>>> _cache = new();
    private readonly Queue<Uri> _cacheOrder = new();
    private readonly object _evictLock = new();

    public Task<Image<Rgba32>?> FetchAsync(Uri url, CancellationToken cancellationToken = default)
    {
        var entry = _cache.GetOrAdd(url, u => new Lazy<Task<Image<Rgba32>?>>(
            () => FetchInternalAsync(u),
            LazyThreadSafetyMode.ExecutionAndPublication));
        TrackInsert(url);
        return entry.Value.WaitAsync(cancellationToken);
    }

    private void TrackInsert(Uri url)
    {
        lock (_evictLock)
        {
            _cacheOrder.Enqueue(url);
            while (_cacheOrder.Count > MaxCacheEntries)
            {
                var oldest = _cacheOrder.Dequeue();
                _cache.TryRemove(oldest, out _);
            }
        }
    }

    private async Task<Image<Rgba32>?> FetchInternalAsync(Uri url)
    {
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            using var cts = new CancellationTokenSource(FetchTimeout);

            using var resp = await http.GetAsync(url, HttpCompletionOption.ResponseHeadersRead, cts.Token).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode)
            {
                logger.LogInformation("Image fetch returned {Status}: {Url}", (int)resp.StatusCode, url);
                return null;
            }

            var mediaType = resp.Content.Headers.ContentType?.MediaType;
            if (mediaType is null || !mediaType.StartsWith("image/", StringComparison.OrdinalIgnoreCase))
            {
                logger.LogInformation("Image fetch skipped — content-type {Media}: {Url}", mediaType ?? "<none>", url);
                return null;
            }

            // Up-front bail when the server tells us how big this is.
            if (resp.Content.Headers.ContentLength is { } len && len > MaxBytes)
            {
                logger.LogInformation("Image fetch skipped — content-length {Len} > {Max}: {Url}", len, MaxBytes, url);
                return null;
            }

            await using var stream = await resp.Content.ReadAsStreamAsync(cts.Token).ConfigureAwait(false);
            using var ms = new MemoryStream(capacity: 64 * 1024);
            var buffer = new byte[16 * 1024];
            int read;
            while ((read = await stream.ReadAsync(buffer.AsMemory(), cts.Token).ConfigureAwait(false)) > 0)
            {
                if (ms.Length + read > MaxBytes)
                {
                    logger.LogInformation("Image fetch exceeded {Max} bytes mid-stream: {Url}", MaxBytes, url);
                    return null;
                }
                ms.Write(buffer, 0, read);
            }

            ms.Position = 0;

            // MaxFrames=1 forces single-frame decode (animated formats decode just the first
            // frame — we can't render animation in cells anyway). Identify before full decode
            // so we can refuse a multi-gigapixel decompression bomb without allocating it.
            var decoderOptions = new DecoderOptions { MaxFrames = 1 };
            var info = await Image.IdentifyAsync(decoderOptions, ms, cts.Token).ConfigureAwait(false);
            if (info is null)
            {
                return null;
            }
            if (info.Width > MaxDimension || info.Height > MaxDimension)
            {
                logger.LogInformation("Image fetch refused — dimensions {W}x{H} exceed {Max}: {Url}",
                    info.Width, info.Height, MaxDimension, url);
                return null;
            }

            ms.Position = 0;
            var image = await Image.LoadAsync<Rgba32>(decoderOptions, ms, cts.Token).ConfigureAwait(false);
            return image;
        }
        catch (OperationCanceledException)
        {
            logger.LogInformation("Image fetch timed out after {Timeout}s: {Url}", FetchTimeout.TotalSeconds, url);
            return null;
        }
        catch (Exception ex)
        {
            logger.LogInformation(ex, "Image fetch failed: {Url}", url);
            return null;
        }
    }
}
