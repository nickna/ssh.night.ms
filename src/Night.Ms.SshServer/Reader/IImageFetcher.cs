using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;

namespace Night.Ms.SshServer.Reader;

// Fetches an http(s) image URL and returns a decoded Image<Rgba32> ready for rendering, or
// null on any failure (transport, non-image content-type, size cap, decode error). Caches
// successfully-fetched images per-process so a second view of the same article (or a link
// hop back to one) doesn't re-fetch and re-decode.
//
// The returned Image is owned by the fetcher's cache — callers must not Dispose it. Mutate
// only via Clone() (which is what the renderer already does for resize).
public interface IImageFetcher
{
    Task<Image<Rgba32>?> FetchAsync(Uri url, CancellationToken cancellationToken = default);
}
