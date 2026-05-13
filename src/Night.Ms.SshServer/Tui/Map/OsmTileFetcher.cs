using System.Collections.Concurrent;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.Formats;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Tui.Map;

// OSM raster-tile fetcher with an in-process FIFO cache. Honours the OSM tile-server usage
// policy in two ways: it sends a project-identifying User-Agent (set on the named
// HttpClient at registration time) and caches aggressively so panning around the same area
// doesn't re-fetch. Per-process cache, not Redis — tiles are tiny and the user count is too.
//
// Singleton: the cache must outlive screen instances so a user re-opening the map screen
// after a pan elsewhere doesn't pay for a re-fetch. Cap is high enough for a full zoom-level
// view across hundreds of pans (~70KB per tile × cap).
internal sealed class OsmTileFetcher(
    IHttpClientFactory httpClientFactory,
    ILogger<OsmTileFetcher> logger)
    : IOsmTileFetcher
{
    public const string HttpClientName = "osm-map-tiles";

    private const int MaxBytes = 256 * 1024; // OSM tiles are typically 10–40 KB; 256 KB is generous
    private const int MaxCacheEntries = 1024; // ~25 MB worst case at 25 KB/tile

    private static readonly TimeSpan FetchTimeout = TimeSpan.FromSeconds(6);

    private readonly ConcurrentDictionary<(int z, int x, int y), Lazy<Task<Image<Rgba32>?>>> _cache = new();
    private readonly Queue<(int z, int x, int y)> _cacheOrder = new();
    private readonly object _evictLock = new();

    public Task<Image<Rgba32>?> FetchAsync(int zoom, int tileX, int tileY, CancellationToken cancellationToken = default)
    {
        var key = (zoom, tileX, tileY);
        var entry = _cache.GetOrAdd(key, k => new Lazy<Task<Image<Rgba32>?>>(
            () => FetchInternalAsync(k.z, k.x, k.y),
            LazyThreadSafetyMode.ExecutionAndPublication));
        TrackInsert(key);
        return entry.Value.WaitAsync(cancellationToken);
    }

    private void TrackInsert((int z, int x, int y) key)
    {
        lock (_evictLock)
        {
            _cacheOrder.Enqueue(key);
            while (_cacheOrder.Count > MaxCacheEntries)
            {
                var oldest = _cacheOrder.Dequeue();
                _cache.TryRemove(oldest, out _);
            }
        }
    }

    private async Task<Image<Rgba32>?> FetchInternalAsync(int z, int x, int y)
    {
        try
        {
            var url = new Uri($"https://tile.openstreetmap.org/{z}/{x}/{y}.png");
            var http = httpClientFactory.CreateClient(HttpClientName);
            using var cts = new CancellationTokenSource(FetchTimeout);

            using var resp = await http.GetAsync(url, HttpCompletionOption.ResponseHeadersRead, cts.Token).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode)
            {
                logger.LogInformation("Tile fetch returned {Status}: z={Z} x={X} y={Y}", (int)resp.StatusCode, z, x, y);
                return null;
            }

            var mediaType = resp.Content.Headers.ContentType?.MediaType;
            if (mediaType is null || !mediaType.StartsWith("image/", StringComparison.OrdinalIgnoreCase))
            {
                logger.LogInformation("Tile fetch skipped — content-type {Media}: z={Z} x={X} y={Y}", mediaType ?? "<none>", z, x, y);
                return null;
            }

            if (resp.Content.Headers.ContentLength is { } len && len > MaxBytes)
            {
                logger.LogInformation("Tile fetch skipped — content-length {Len} > {Max}: z={Z} x={X} y={Y}", len, MaxBytes, z, x, y);
                return null;
            }

            await using var stream = await resp.Content.ReadAsStreamAsync(cts.Token).ConfigureAwait(false);
            using var ms = new MemoryStream(capacity: 32 * 1024);
            var buffer = new byte[16 * 1024];
            int read;
            while ((read = await stream.ReadAsync(buffer.AsMemory(), cts.Token).ConfigureAwait(false)) > 0)
            {
                if (ms.Length + read > MaxBytes)
                {
                    logger.LogInformation("Tile fetch exceeded {Max} bytes mid-stream: z={Z} x={X} y={Y}", MaxBytes, z, x, y);
                    return null;
                }
                ms.Write(buffer, 0, read);
            }

            ms.Position = 0;
            var decoderOptions = new DecoderOptions { MaxFrames = 1 };
            var image = await Image.LoadAsync<Rgba32>(decoderOptions, ms, cts.Token).ConfigureAwait(false);
            return image;
        }
        catch (OperationCanceledException)
        {
            logger.LogInformation("Tile fetch timed out: z={Z} x={X} y={Y}", z, x, y);
            return null;
        }
        catch (Exception ex)
        {
            logger.LogInformation(ex, "Tile fetch failed: z={Z} x={X} y={Y}", z, x, y);
            return null;
        }
    }
}
