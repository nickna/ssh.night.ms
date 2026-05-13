namespace Night.Ms.SshServer.Tui.Map;

// Fetches a single OpenMapTiles-schema vector tile as a parsed layer set. Returns null on
// any failure (transport, 404 for an empty world cell, decode error) so the renderer can
// fall back to painting "no data" without halting the screen.
//
// Implementations cache decoded tiles per-process — vector tile decode (protobuf + zigzag +
// command stream) is hot CPU, so re-decoding on every pan would burn the server.
internal interface IVectorTileFetcher
{
    Task<DecodedVectorTile?> FetchAsync(int zoom, int tileX, int tileY, CancellationToken cancellationToken = default);
}

// Decoded MVT: list of layers, each layer is name + extent + list of features. The features
// already have geometry in tile-local 0..extent coords and a flat attribute dict. We keep
// only the layers + features the renderer style asks for — everything else is dropped at
// decode time to keep the cache small.
internal sealed record DecodedVectorTile(int Zoom, int TileX, int TileY, IReadOnlyList<DecodedLayer> Layers);

internal sealed record DecodedLayer(string Name, int Extent, IReadOnlyList<DecodedFeature> Features);

internal sealed record DecodedFeature(
    DecodedGeometryKind Kind,
    // For Polygons, each ArraySegment in Rings is one ring; outer rings are CW and inner
    // rings (holes) are CCW per MVT spec (after Y-axis flip). For Lines/Points, each segment
    // is one line / one point group.
    IReadOnlyList<DecodedRing> Rings,
    IReadOnlyDictionary<string, object?> Attributes);

internal enum DecodedGeometryKind { Point, Line, Polygon }

// Tile-local coords in the 0..Extent range. The MVT spec runs Y top-down (same as screen),
// so no axis flip is needed when we map to render pixels.
internal sealed record DecodedRing(IReadOnlyList<TilePoint> Points);

internal readonly record struct TilePoint(int X, int Y);
