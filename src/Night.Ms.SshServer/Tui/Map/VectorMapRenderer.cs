using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Map.Braille;

namespace Night.Ms.SshServer.Tui.Map;

// Builds the visible map as a BrailleCanvas: pulls all MVTs covering the viewport, then
// paints layers in fixed style order (background → water → transportation → place labels)
// so later layers stack on top. Coordinates are transformed tile-local → canvas-subpixel
// via the viewport's TileCover (which already accounts for crop offset).
//
// Style is hardcoded (three layers) — Mapbox Style JSON parsing is a follow-up. Colours
// were picked to read as "OpenMapTiles dark" on a typical xterm: dark navy water, pale
// blue-grey roads with a brighter motorway accent, bright yellow city labels.
internal static class VectorMapRenderer
{
    // Background "land" colour for cells outside any drawn polygon.
    private static readonly ArtColor LandColor = new(0x18, 0x1C, 0x24);

    // Water fill: a desaturated navy that contrasts with land + road colours without
    // saturating the screen on big ocean tiles.
    private static readonly ArtColor WaterColor = new(0x2A, 0x3F, 0x5F);

    // Road colours by OpenMapTiles `class` attribute. Major roads pop bright so the eye
    // catches the routes first; residential/minor sit dimmer so cities don't strobe.
    private static readonly ArtColor RoadMotorway   = new(0xE8, 0xC8, 0x88);
    private static readonly ArtColor RoadPrimary    = new(0xC8, 0xC0, 0xB0);
    private static readonly ArtColor RoadMinor      = new(0x68, 0x70, 0x80);
    private static readonly ArtColor RailwayColor   = new(0x80, 0x88, 0x98);

    private static readonly ArtColor PlaceCityLabel  = new(0xF0, 0xE0, 0xA0);
    private static readonly ArtColor PlaceTownLabel  = new(0xC0, 0xC8, 0xC0);
    private static readonly ArtColor PlaceOtherLabel = new(0x88, 0x90, 0x98);

    public static async Task<CellGrid?> RenderAsync(
        MapViewport viewport,
        IVectorTileFetcher fetcher,
        CancellationToken cancellationToken)
    {
        var cover = viewport.Cover();
        if (cover.Tiles.Count == 0 || cover.CropWidth <= 0 || cover.CropHeight <= 0) return null;

        // Canvas dims: subpixel grid mirrors viewport pixel rect. The viewport passed in
        // must size itself to (2*cols, 4*rows) so this divides cleanly — MapScreen does.
        var cellCols = cover.CropWidth  / BrailleCanvas.SubPixelsX;
        var cellRows = cover.CropHeight / BrailleCanvas.SubPixelsY;
        if (cellCols == 0 || cellRows == 0) return null;

        using var sem = new SemaphoreSlim(4, 4);
        var fetched = new (TileSlot Slot, DecodedVectorTile? Tile)[cover.Tiles.Count];
        var tasks = cover.Tiles.Select(async (slot, i) =>
        {
            await sem.WaitAsync(cancellationToken).ConfigureAwait(false);
            try
            {
                var tile = await fetcher.FetchAsync(cover.Zoom, slot.TileX, slot.TileY, cancellationToken).ConfigureAwait(false);
                fetched[i] = (slot, tile);
            }
            finally { sem.Release(); }
        }).ToArray();

        try { await Task.WhenAll(tasks).ConfigureAwait(false); }
        catch (OperationCanceledException) { return null; }
        cancellationToken.ThrowIfCancellationRequested();

        var canvas = new BrailleCanvas(cellCols, cellRows);
        canvas.Clear(LandColor);

        // Layer 1: water polygons. Drawn first so everything else stacks above.
        foreach (var (slot, tile) in fetched)
        {
            if (tile is null) continue;
            DrawLayer(canvas, tile, slot, cover, "water", DrawWaterFeature);
        }
        // Layer 2: roads + rail.
        foreach (var (slot, tile) in fetched)
        {
            if (tile is null) continue;
            DrawLayer(canvas, tile, slot, cover, "transportation", DrawTransportationFeature);
        }
        // Layer 3: place labels — paint last so they sit above geometry and so the label
        // collision tracker covers every place at once.
        var usedLabelCells = new HashSet<(int x, int y)>();
        foreach (var (slot, tile) in fetched)
        {
            if (tile is null) continue;
            DrawLabelsLayer(canvas, tile, slot, cover, usedLabelCells);
        }

        return canvas.ToCellGrid();
    }

    private static void DrawLayer(
        BrailleCanvas canvas,
        DecodedVectorTile tile,
        TileSlot slot,
        TileCover cover,
        string layerName,
        Action<BrailleCanvas, DecodedFeature, int, int, int> drawFeature)
    {
        var layer = tile.Layers.FirstOrDefault(l => l.Name == layerName);
        if (layer is null) return;
        var originX = slot.OffsetX - cover.CropX;
        var originY = slot.OffsetY - cover.CropY;
        var extent = layer.Extent;
        foreach (var feature in layer.Features)
        {
            drawFeature(canvas, feature, originX, originY, extent);
        }
    }

    private static void DrawWaterFeature(BrailleCanvas canvas, DecodedFeature f, int originX, int originY, int extent)
    {
        if (f.Kind != DecodedGeometryKind.Polygon || f.Rings.Count == 0) return;
        var rings = new List<IReadOnlyList<(int, int)>>(f.Rings.Count);
        foreach (var ring in f.Rings)
        {
            var pts = new (int, int)[ring.Points.Count];
            for (var i = 0; i < ring.Points.Count; i++)
            {
                var p = ring.Points[i];
                pts[i] = ProjectTilePoint(p, originX, originY, extent);
            }
            rings.Add(pts);
        }
        Draw.FillPolygon(canvas, rings, WaterColor);
    }

    private static void DrawTransportationFeature(BrailleCanvas canvas, DecodedFeature f, int originX, int originY, int extent)
    {
        if (f.Kind != DecodedGeometryKind.Line) return;

        var cls = f.Attributes.TryGetValue("class", out var c) ? c as string : null;
        var color = ClassifyRoadColor(cls);

        foreach (var ring in f.Rings)
        {
            if (ring.Points.Count < 2) continue;
            var pts = new (int X, int Y)[ring.Points.Count];
            for (var i = 0; i < ring.Points.Count; i++)
            {
                pts[i] = ProjectTilePoint(ring.Points[i], originX, originY, extent);
            }
            Draw.DrawLineStrip(canvas, pts, color);
        }
    }

    private static ArtColor ClassifyRoadColor(string? cls) => cls switch
    {
        // OpenMapTiles transportation `class` values: motorway, trunk, primary, secondary,
        // tertiary, minor, service, track, path, rail, transit, ...
        "motorway" or "trunk" => RoadMotorway,
        "primary" or "secondary" => RoadPrimary,
        "rail" or "transit" => RailwayColor,
        _ => RoadMinor,
    };

    private static void DrawLabelsLayer(
        BrailleCanvas canvas,
        DecodedVectorTile tile,
        TileSlot slot,
        TileCover cover,
        HashSet<(int x, int y)> usedLabelCells)
    {
        var layer = tile.Layers.FirstOrDefault(l => l.Name == "place");
        if (layer is null) return;
        var originX = slot.OffsetX - cover.CropX;
        var originY = slot.OffsetY - cover.CropY;
        var extent = layer.Extent;
        foreach (var feature in layer.Features)
        {
            if (feature.Kind != DecodedGeometryKind.Point) continue;
            if (feature.Rings.Count == 0 || feature.Rings[0].Points.Count == 0) continue;

            // Pick anchor: first point of first ring (MVT point features carry exactly one
            // anchor in practice, but the spec allows multipoint).
            var anchor = feature.Rings[0].Points[0];
            var (px, py) = ProjectTilePoint(anchor, originX, originY, extent);

            var labelText = PickLabel(feature.Attributes);
            if (labelText is null) continue;

            var cls = feature.Attributes.TryGetValue("class", out var c) ? c as string : null;
            var color = cls switch
            {
                "continent" or "country" or "state" or "province" => PlaceCityLabel,
                "city" => PlaceCityLabel,
                "town" => PlaceTownLabel,
                _ => PlaceOtherLabel,
            };

            var cellX = px / BrailleCanvas.SubPixelsX;
            var cellY = py / BrailleCanvas.SubPixelsY;

            // Centre roughly on the anchor; clip to label width so a city near the right
            // edge still shows a few letters rather than nothing.
            var startX = cellX - labelText.Length / 2;
            if (startX + labelText.Length <= 0 || startX >= canvas.CellCols) continue;
            if (cellY < 0 || cellY >= canvas.CellRows) continue;

            // Greedy collision: skip the label if any cell it would occupy is already used.
            // Imperfect (a tall string of cities sometimes drops the last one) but cheap and
            // good enough for the v1 visual.
            var collided = false;
            for (var i = 0; i < labelText.Length; i++)
            {
                var cx = startX + i;
                if (cx < 0 || cx >= canvas.CellCols) continue;
                if (usedLabelCells.Contains((cx, cellY))) { collided = true; break; }
            }
            if (collided) continue;
            for (var i = 0; i < labelText.Length; i++)
            {
                var cx = startX + i;
                if (cx >= 0 && cx < canvas.CellCols) usedLabelCells.Add((cx, cellY));
            }

            Draw.DrawString(canvas, startX, cellY, labelText, color);
        }
    }

    // Prefer English-tagged labels when available — keeps the BBS readable for the typical
    // user. Falls back to the OpenStreetMap `name` (often Latin / source-language) and skips
    // anything else (e.g. an entry that only carries `name:zh`).
    private static string? PickLabel(IReadOnlyDictionary<string, object?> attrs)
    {
        if (attrs.TryGetValue("name_en", out var en) && en is string s1 && !string.IsNullOrWhiteSpace(s1)) return s1;
        if (attrs.TryGetValue("name:en", out var en2) && en2 is string s2 && !string.IsNullOrWhiteSpace(s2)) return s2;
        if (attrs.TryGetValue("name:latin", out var lat) && lat is string s3 && !string.IsNullOrWhiteSpace(s3)) return s3;
        if (attrs.TryGetValue("name", out var nm) && nm is string s4 && !string.IsNullOrWhiteSpace(s4)) return s4;
        return null;
    }

    private static (int X, int Y) ProjectTilePoint(TilePoint p, int originX, int originY, int extent)
    {
        // Tile is 256 world-pixels per axis; map tile-local 0..extent → 0..256 → composite.
        // Using long for the multiply to keep z=18 + extent=4096 from overflowing on a
        // 32-bit accumulator.
        var x = (int)(originX + (long)p.X * MapViewport.TileSize / extent);
        var y = (int)(originY + (long)p.Y * MapViewport.TileSize / extent);
        return (x, y);
    }
}
