using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Tui.Map;

// Fetches a single 256×256 raster tile from an OSM-compatible tile server. Returns null on
// any failure (transport, non-image content-type, decode error) so the renderer can paint
// a placeholder without halting the screen.
//
// Implementations are expected to cache fetched tiles per-process — panning around the same
// area shouldn't re-issue HTTP requests, both for latency and to honour the OSM tile usage
// policy.
internal interface IOsmTileFetcher
{
    Task<Image<Rgba32>?> FetchAsync(int zoom, int tileX, int tileY, CancellationToken cancellationToken = default);
}
