using Night.Ms.Imaging;
using Night.Ms.SshServer.Tui.Art;
using SixLabors.ImageSharp;
using SixLabors.ImageSharp.PixelFormats;
using SixLabors.ImageSharp.Processing;

namespace Night.Ms.SshServer.Tui.Map;

// Pulls the set of tiles a viewport needs, blits them into one image, crops to the
// viewport rect, and converts the result into a CellGrid via the half-block pipeline used
// elsewhere (HalfBlockRenderer → SgrParser → CellGrid). Keeping the ANSI roundtrip means
// the cells coming back are exactly what AnsiArtView already paints — no new render path.
//
// Missing tiles (fetch failed, off the world rail, etc.) render as the dark-gray fill the
// composite was initialized with. Failure of one tile must not pull down the whole frame.
internal static class MapRenderer
{
    // Fill colour for missing/in-flight tiles. Dark grey-blue reads as "ocean / nothing"
    // against typical OSM raster so an absent tile doesn't dominate the viewport.
    private static readonly Rgba32 MissingTileFill = new(0x1A, 0x1F, 0x2A);

    public static async Task<CellGrid?> RenderAsync(
        MapViewport viewport,
        IOsmTileFetcher fetcher,
        CancellationToken cancellationToken)
    {
        var cover = viewport.Cover();
        if (cover.Tiles.Count == 0 || cover.CropWidth <= 0 || cover.CropHeight <= 0)
        {
            return null;
        }

        // Composite tiles in parallel. Cap concurrency so a deep zoom doesn't fan out to
        // dozens of simultaneous OSM hits — the policy frowns on bursty parallel fetches.
        using var sem = new SemaphoreSlim(4, 4);
        var fetched = new (TileSlot Slot, Image<Rgba32>? Image)[cover.Tiles.Count];
        var tasks = cover.Tiles.Select(async (slot, i) =>
        {
            await sem.WaitAsync(cancellationToken).ConfigureAwait(false);
            try
            {
                var img = await fetcher.FetchAsync(cover.Zoom, slot.TileX, slot.TileY, cancellationToken).ConfigureAwait(false);
                fetched[i] = (slot, img);
            }
            finally
            {
                sem.Release();
            }
        }).ToArray();

        try
        {
            await Task.WhenAll(tasks).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            return null;
        }

        cancellationToken.ThrowIfCancellationRequested();

        // Compose, crop, half-block. Image<Rgba32> ctor allocates a contiguous buffer; we
        // dispose it as soon as we have the ANSI string so the renderer doesn't hold two
        // copies of the bitmap.
        string ansi;
        using (var composite = new Image<Rgba32>(cover.CompositeWidth, cover.CompositeHeight, MissingTileFill))
        {
            foreach (var (slot, img) in fetched)
            {
                if (img is null) continue;
                composite.Mutate(ctx => ctx.DrawImage(img, new Point(slot.OffsetX, slot.OffsetY), 1f));
            }

            // Crop to the viewport's pixel rect. The composite is always large enough — the
            // viewport's tile cover by construction includes every tile that intersects.
            var cropRect = new Rectangle(cover.CropX, cover.CropY, cover.CropWidth, cover.CropHeight);
            composite.Mutate(ctx => ctx.Crop(cropRect));

            // HalfBlockRenderer wants targetCols and works out the row count from the input
            // aspect. Our cropped image is exactly (CropWidth, 2*rows) — passing CropWidth
            // round-trips to a CellGrid of (CropWidth, rows). Truecolor + no dither matches
            // the existing inline-image path in ReaderScreen so output looks consistent.
            ansi = HalfBlockRenderer.Render(composite, cover.CropWidth, ColorDepth.Truecolor, DitherMode.None);
        }

        return SgrParser.Parse(ansi);
    }
}
