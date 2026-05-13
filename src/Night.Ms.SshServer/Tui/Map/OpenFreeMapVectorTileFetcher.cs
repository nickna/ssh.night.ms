using System.Collections.Concurrent;
using System.Net;
using System.Text.Json;
using Mapbox.Vector.Tile;

namespace Night.Ms.SshServer.Tui.Map;

// OpenFreeMap MVT fetcher. Two responsibilities:
//   1. Discover the versioned tile-URL template via the planet TileJSON endpoint — OFM
//      embeds a snapshot date in the path (e.g. /planet/20260506_001001_pt/{z}/{x}/{y}.pbf)
//      and the bare /planet/{z}/{x}/{y}.pbf URL returns an empty body.
//   2. Fetch, parse, and project tiles into a DecodedVectorTile keeping only the layers the
//      renderer's style consumes (water, transportation, place). Cache decoded tiles per
//      (z,x,y) so panning doesn't re-decode protobuf+zigzag command streams.
//
// OpenFreeMap policy: no API key, no rate limits, attribution required ("© OpenStreetMap").
// HttpClient is configured at registration with AutomaticDecompression for gzip pbf bodies.
internal sealed class OpenFreeMapVectorTileFetcher(
    IHttpClientFactory httpClientFactory,
    ILogger<OpenFreeMapVectorTileFetcher> logger)
    : IVectorTileFetcher
{
    public const string HttpClientName = "openfreemap-vector";
    private const string TileJsonUrl = "https://tiles.openfreemap.org/planet";

    // Layers we actually paint. Anything else gets stripped at decode-time so the cache
    // entry is a fraction of the raw MVT — OpenMapTiles ships ~40 layers, we keep three.
    private static readonly HashSet<string> KeptLayers = new(StringComparer.Ordinal)
    {
        "water",          // polygon: large blue fill
        "transportation", // line: roads / rail
        "place",          // point: settlement labels
    };

    private const int MaxBytes = 2 * 1024 * 1024; // generous: a busy z14 tile is ~500KB
    private const int MaxCacheEntries = 1024;
    private static readonly TimeSpan FetchTimeout = TimeSpan.FromSeconds(8);

    private readonly ConcurrentDictionary<(int z, int x, int y), Lazy<Task<DecodedVectorTile?>>> _cache = new();
    private readonly Queue<(int z, int x, int y)> _cacheOrder = new();
    private readonly object _evictLock = new();

    // Tile URL template lookup: discovered once via TileJSON, refreshed on 404 (snapshot
    // rotation). The lock serialises concurrent discovery on cold start so we don't burn
    // N TileJSON requests when N pan operations hit at the same time.
    private string? _tileUrlTemplate;
    private readonly SemaphoreSlim _templateLock = new(1, 1);

    public Task<DecodedVectorTile?> FetchAsync(int zoom, int tileX, int tileY, CancellationToken cancellationToken = default)
    {
        var key = (zoom, tileX, tileY);
        var entry = _cache.GetOrAdd(key, k => new Lazy<Task<DecodedVectorTile?>>(
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

    private async Task<DecodedVectorTile?> FetchInternalAsync(int z, int x, int y)
    {
        try
        {
            var http = httpClientFactory.CreateClient(HttpClientName);
            using var cts = new CancellationTokenSource(FetchTimeout);
            var template = await EnsureTemplateAsync(http, cts.Token).ConfigureAwait(false);
            if (template is null) return null;

            var url = new Uri(template
                .Replace("{z}", z.ToString())
                .Replace("{x}", x.ToString())
                .Replace("{y}", y.ToString()));

            using var resp = await http.GetAsync(url, HttpCompletionOption.ResponseHeadersRead, cts.Token).ConfigureAwait(false);
            if (resp.StatusCode == HttpStatusCode.NotFound)
            {
                // Most common cause: snapshot rotated and our cached template is stale.
                // Re-discover the template and try one more time on the same request.
                _tileUrlTemplate = null;
                var refreshed = await EnsureTemplateAsync(http, cts.Token).ConfigureAwait(false);
                if (refreshed is null || refreshed == template) return null;

                url = new Uri(refreshed
                    .Replace("{z}", z.ToString())
                    .Replace("{x}", x.ToString())
                    .Replace("{y}", y.ToString()));
                using var retry = await http.GetAsync(url, HttpCompletionOption.ResponseHeadersRead, cts.Token).ConfigureAwait(false);
                if (!retry.IsSuccessStatusCode) return null;
                return await DecodeAsync(retry, z, x, y, cts.Token).ConfigureAwait(false);
            }
            if (!resp.IsSuccessStatusCode)
            {
                logger.LogInformation("MVT fetch returned {Status}: z={Z} x={X} y={Y}", (int)resp.StatusCode, z, x, y);
                return null;
            }

            return await DecodeAsync(resp, z, x, y, cts.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            logger.LogInformation("MVT fetch timed out: z={Z} x={X} y={Y}", z, x, y);
            return null;
        }
        catch (Exception ex)
        {
            logger.LogInformation(ex, "MVT fetch failed: z={Z} x={X} y={Y}", z, x, y);
            return null;
        }
    }

    private async Task<DecodedVectorTile?> DecodeAsync(HttpResponseMessage resp, int z, int x, int y, CancellationToken ct)
    {
        var contentLength = resp.Content.Headers.ContentLength;
        if (contentLength is { } len && len > MaxBytes)
        {
            logger.LogInformation("MVT fetch skipped — content-length {Len} > {Max}: z={Z} x={X} y={Y}", len, MaxBytes, z, x, y);
            return null;
        }

        // Empty body = "no data here" (ocean, off-world). Treat as a successful empty tile
        // so the renderer can still draw whatever the lower-zoom backdrop provides.
        if (contentLength == 0)
        {
            return new DecodedVectorTile(z, x, y, Array.Empty<DecodedLayer>());
        }

        await using var stream = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        using var ms = new MemoryStream(capacity: 64 * 1024);
        var buffer = new byte[16 * 1024];
        int read;
        while ((read = await stream.ReadAsync(buffer.AsMemory(), ct).ConfigureAwait(false)) > 0)
        {
            if (ms.Length + read > MaxBytes)
            {
                logger.LogInformation("MVT fetch exceeded {Max} bytes mid-stream: z={Z} x={X} y={Y}", MaxBytes, z, x, y);
                return null;
            }
            ms.Write(buffer, 0, read);
        }
        if (ms.Length == 0)
        {
            return new DecodedVectorTile(z, x, y, Array.Empty<DecodedLayer>());
        }
        ms.Position = 0;

        var layers = VectorTileParser.Parse(ms);
        var kept = new List<DecodedLayer>(KeptLayers.Count);
        foreach (var layer in layers)
        {
            if (!KeptLayers.Contains(layer.Name)) continue;
            var features = new List<DecodedFeature>(layer.VectorTileFeatures.Count);
            foreach (var f in layer.VectorTileFeatures)
            {
                var kind = f.GeometryType switch
                {
                    Tile.GeomType.Point      => DecodedGeometryKind.Point,
                    Tile.GeomType.LineString => DecodedGeometryKind.Line,
                    Tile.GeomType.Polygon    => DecodedGeometryKind.Polygon,
                    _ => (DecodedGeometryKind?)null,
                };
                if (kind is not { } gk) continue;

                var rings = new List<DecodedRing>(f.Geometry.Count);
                foreach (var seg in f.Geometry)
                {
                    var pts = new TilePoint[seg.Count];
                    for (var i = 0; i < seg.Count; i++)
                    {
                        var c = seg.Array![seg.Offset + i];
                        pts[i] = new TilePoint((int)c.X, (int)c.Y);
                    }
                    rings.Add(new DecodedRing(pts));
                }

                // Attributes list comes back as KeyValuePair<string, object>; we want a dict
                // for O(1) style lookups in the renderer ("name", "class", etc).
                Dictionary<string, object?>? attrs = null;
                if (f.Attributes is { Count: > 0 })
                {
                    attrs = new Dictionary<string, object?>(f.Attributes.Count, StringComparer.Ordinal);
                    foreach (var kv in f.Attributes) attrs[kv.Key] = kv.Value;
                }

                features.Add(new DecodedFeature(gk, rings, attrs ?? EmptyAttrs));
            }
            kept.Add(new DecodedLayer(layer.Name, (int)layer.Extent, features));
        }
        return new DecodedVectorTile(z, x, y, kept);
    }

    private static readonly IReadOnlyDictionary<string, object?> EmptyAttrs =
        new Dictionary<string, object?>(0);

    private async Task<string?> EnsureTemplateAsync(HttpClient http, CancellationToken ct)
    {
        if (_tileUrlTemplate is { } cached) return cached;
        await _templateLock.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (_tileUrlTemplate is { } cachedAgain) return cachedAgain;

            using var resp = await http.GetAsync(new Uri(TileJsonUrl), HttpCompletionOption.ResponseHeadersRead, ct).ConfigureAwait(false);
            if (!resp.IsSuccessStatusCode)
            {
                logger.LogWarning("OpenFreeMap TileJSON returned {Status}", (int)resp.StatusCode);
                return null;
            }
            await using var s = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
            using var doc = await JsonDocument.ParseAsync(s, cancellationToken: ct).ConfigureAwait(false);
            if (!doc.RootElement.TryGetProperty("tiles", out var tilesEl) || tilesEl.ValueKind != JsonValueKind.Array || tilesEl.GetArrayLength() == 0)
            {
                logger.LogWarning("OpenFreeMap TileJSON missing 'tiles' array");
                return null;
            }
            var template = tilesEl[0].GetString();
            if (string.IsNullOrEmpty(template))
            {
                logger.LogWarning("OpenFreeMap TileJSON has empty tile URL");
                return null;
            }
            _tileUrlTemplate = template;
            logger.LogInformation("OpenFreeMap tile URL template resolved: {Template}", template);
            return template;
        }
        finally
        {
            _templateLock.Release();
        }
    }
}
