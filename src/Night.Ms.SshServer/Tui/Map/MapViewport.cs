namespace Night.Ms.SshServer.Tui.Map;

// Slippy-tile viewport: keeps the (lat, lon, zoom) center and translates that into the set
// of 256×256 tiles the renderer needs to fetch plus the pixel crop that lands on screen.
// Pure math — no I/O — so it's trivially testable and the renderer can be swapped without
// touching the projection logic.
//
// Coordinate convention follows OSM slippy-tile (Web Mercator, EPSG:3857):
//   n = 2^zoom
//   tx = (lon + 180) / 360 * n
//   ty = (1 − asinh(tan(lat)) / π) / 2 * n
// At zoom z, the world is (n * 256) pixels square. We track the viewport in *world pixels*
// because pan/zoom math is far simpler there than in lat/lon.
internal sealed class MapViewport
{
    public const int TileSize = 256;
    public const int MinZoom = 1;
    public const int MaxZoom = 18; // OSM raster tile-policy ceiling; deeper zooms need vector

    // Web Mercator clamps near ±85.0511° — beyond that, ty diverges. Mirrors what every
    // slippy map library uses as a latitude rail.
    private const double LatMin = -85.05112878;
    private const double LatMax = 85.05112878;

    public double CenterLat { get; private set; }
    public double CenterLon { get; private set; }
    public int Zoom { get; private set; }

    // Pixel size of the rendered map area. The screen tells us how many half-block cells it
    // can spare — each cell is 1 source pixel wide × 2 source pixels tall, so a (cols, rows)
    // viewport is (cols, 2 * rows) world pixels.
    public int PixelWidth { get; private set; }
    public int PixelHeight { get; private set; }

    public MapViewport(double centerLat, double centerLon, int zoom, int pixelWidth, int pixelHeight)
    {
        CenterLat = ClampLat(centerLat);
        CenterLon = WrapLon(centerLon);
        Zoom = Math.Clamp(zoom, MinZoom, MaxZoom);
        PixelWidth = Math.Max(1, pixelWidth);
        PixelHeight = Math.Max(1, pixelHeight);
    }

    public void Resize(int pixelWidth, int pixelHeight)
    {
        PixelWidth = Math.Max(1, pixelWidth);
        PixelHeight = Math.Max(1, pixelHeight);
    }

    // Pan by a number of *screen pixels* in each direction. Positive dx moves the viewport
    // right (i.e. the map content slides left). Latitude is clamped to the Mercator rail;
    // longitude wraps around the antimeridian.
    public void Pan(int dxPixels, int dyPixels)
    {
        var (px, py) = LonLatToWorldPixels(CenterLon, CenterLat, Zoom);
        px += dxPixels;
        py += dyPixels;
        var (lon, lat) = WorldPixelsToLonLat(px, py, Zoom);
        CenterLon = WrapLon(lon);
        CenterLat = ClampLat(lat);
    }

    public void ZoomIn()  => Zoom = Math.Min(Zoom + 1, MaxZoom);
    public void ZoomOut() => Zoom = Math.Max(Zoom - 1, MinZoom);

    // Tiles needed to cover the viewport plus the pixel crop within the composited mosaic.
    // The renderer fetches each tile, blits them at (tile.OffsetX, tile.OffsetY) inside an
    // image of (CompositeWidth, CompositeHeight), then crops (CropX, CropY, PixelWidth,
    // PixelHeight). The crop guarantees the centre of the viewport lands at the centre of
    // the output.
    public TileCover Cover()
    {
        var (cx, cy) = LonLatToWorldPixels(CenterLon, CenterLat, Zoom);
        var halfW = PixelWidth / 2.0;
        var halfH = PixelHeight / 2.0;
        var left = cx - halfW;
        var top  = cy - halfH;

        // Tile index range (inclusive). Floor on left/top, ceiling on right/bottom so
        // partial tiles at the edges are still fetched.
        var firstTx = (int)Math.Floor(left / TileSize);
        var firstTy = (int)Math.Floor(top  / TileSize);
        var lastTx  = (int)Math.Floor((left + PixelWidth  - 1) / TileSize);
        var lastTy  = (int)Math.Floor((top  + PixelHeight - 1) / TileSize);

        var n = 1 << Zoom; // world is n tiles wide / tall
        var tiles = new List<TileSlot>((lastTx - firstTx + 1) * (lastTy - firstTy + 1));
        for (var ty = firstTy; ty <= lastTy; ty++)
        {
            if (ty < 0 || ty >= n) continue; // off the top/bottom of the world — leave a gap
            for (var tx = firstTx; tx <= lastTx; tx++)
            {
                // Wrap longitude tile index so panning past 180°/-180° still pulls real tiles.
                var wrappedTx = ((tx % n) + n) % n;
                tiles.Add(new TileSlot(
                    TileX: wrappedTx,
                    TileY: ty,
                    OffsetX: (tx - firstTx) * TileSize,
                    OffsetY: (ty - firstTy) * TileSize));
            }
        }

        var compositeWidth  = (lastTx - firstTx + 1) * TileSize;
        var compositeHeight = (lastTy - firstTy + 1) * TileSize;
        var cropX = (int)Math.Round(left - firstTx * TileSize);
        var cropY = (int)Math.Round(top  - firstTy * TileSize);

        return new TileCover(
            Zoom: Zoom,
            Tiles: tiles,
            CompositeWidth: compositeWidth,
            CompositeHeight: compositeHeight,
            CropX: cropX,
            CropY: cropY,
            CropWidth: PixelWidth,
            CropHeight: PixelHeight);
    }

    // --- projection helpers -------------------------------------------------

    private static (double px, double py) LonLatToWorldPixels(double lon, double lat, int zoom)
    {
        var n = (double)(1L << zoom);
        var sin = Math.Sin(lat * Math.PI / 180.0);
        // Equivalent to (1 − asinh(tan(latRad)) / π) / 2; written via log for numerical
        // stability near the poles.
        var ty = (0.5 - Math.Log((1 + sin) / (1 - sin)) / (4 * Math.PI)) * n;
        var tx = ((lon + 180.0) / 360.0) * n;
        return (tx * TileSize, ty * TileSize);
    }

    private static (double lon, double lat) WorldPixelsToLonLat(double px, double py, int zoom)
    {
        var n = (double)(1L << zoom);
        var tx = px / TileSize;
        var ty = py / TileSize;
        var lon = tx / n * 360.0 - 180.0;
        var latRad = Math.Atan(Math.Sinh(Math.PI * (1 - 2 * ty / n)));
        var lat = latRad * 180.0 / Math.PI;
        return (lon, lat);
    }

    private static double ClampLat(double lat) => Math.Clamp(lat, LatMin, LatMax);

    private static double WrapLon(double lon)
    {
        // Normalize to [-180, 180). Modulo on doubles is fiddly — keep it explicit.
        lon = ((lon + 180.0) % 360.0 + 360.0) % 360.0 - 180.0;
        return lon;
    }
}

internal readonly record struct TileSlot(int TileX, int TileY, int OffsetX, int OffsetY);

internal readonly record struct TileCover(
    int Zoom,
    IReadOnlyList<TileSlot> Tiles,
    int CompositeWidth,
    int CompositeHeight,
    int CropX,
    int CropY,
    int CropWidth,
    int CropHeight);
