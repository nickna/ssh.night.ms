using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Map;
using Night.Ms.SshServer.Tui.Map.Braille;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Interactive map: pulls tiles for the current viewport, renders to a CellGrid, hands off
// to AnsiArtView. Two rendering modes — vector (OpenFreeMap MVT into a BrailleCanvas; sharp
// roads + legible city labels) and raster (OSM PNG tiles via half-block; mud at 80×24 but
// always renders). Vector is the default; `V` toggles. Pan with arrows / hjkl, zoom +/-,
// reset R, exit Q/Esc.
//
// The screen is render-mode-agnostic from the user's perspective — same controls, same
// viewport state — so we can swap rendering pipelines (or pick the mode best suited to the
// PTY size) without changing the navigation contract.
internal sealed class MapScreen : BbsWindow
{
    private const double DefaultLat = 37.7749;   // San Francisco — recognisable at z11
    private const double DefaultLon = -122.4194;
    private const int DefaultZoom = 11;
    private const double PanFraction = 0.25;     // each arrow pan = 1/4 of the viewport

    private readonly IApplication _app;
    private readonly IOsmTileFetcher _rasterTiles;
    private readonly IVectorTileFetcher _vectorTiles;
    private readonly ILogger<MapScreen> _logger;

    private readonly Label _title;
    private readonly AnsiArtView _artView;
    private readonly Label _attribution;
    private readonly Label _hint;

    private MapViewport _viewport;
    private RenderMode _mode = RenderMode.Vector;
    private CancellationTokenSource? _renderCts;
    private int _renderGeneration;

    private enum RenderMode { Vector, Raster }

    public MapScreen(
        IApplication app,
        IServiceProvider services,
        User user,
        IOsmTileFetcher rasterTiles,
        IVectorTileFetcher vectorTiles,
        ILogger<MapScreen> logger)
        : base(app, services, user)
    {
        _app = app;
        _rasterTiles = rasterTiles;
        _vectorTiles = vectorTiles;
        _logger = logger;

        Title = "ssh.night.ms — map";

        _title = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "loading map...",
        };
        _title.SetScheme(BbsTheme.Header_);

        _artView = new AnsiArtView { X = 0, Y = 2 };

        _attribution = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(3),
            Width = Dim.Fill(),
            Text = "Map data © OpenStreetMap contributors via OpenFreeMap — openfreemap.org",
        };
        _attribution.SetScheme(BbsTheme.Faint_);

        _hint = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
            Text = HintFor(RenderMode.Vector),
        };
        _hint.SetScheme(BbsTheme.Hint);

        Add(_title, _artView, _attribution, _hint);

        // Placeholder viewport — sized properly once layout has run. We seed with vector
        // dimensions (2×4 subpixels/cell) so re-sizing later doesn't churn the cache when
        // the user opens the screen and stays in vector mode.
        _viewport = new MapViewport(DefaultLat, DefaultLon, DefaultZoom, pixelWidth: 160, pixelHeight: 72);

        Initialized += (_, _) =>
        {
            ResyncViewportSize();
            QueueRender();
        };

        KeyDown += OnKey;
    }

    private void OnKey(object? _, Key key)
    {
        if (key == Key.Esc || key == Key.Q || key == Key.Q.WithShift)
        {
            CancelInFlight();
            _app.RequestStop();
            key.Handled = true;
            return;
        }

        if (key == Key.V || key == Key.V.WithShift)
        {
            _mode = _mode == RenderMode.Vector ? RenderMode.Raster : RenderMode.Vector;
            _hint.Text = HintFor(_mode);
            ResyncViewportSize(); // raster + vector want different pixel dims per cell
            QueueRender();
            key.Handled = true;
            return;
        }

        var (panDx, panDy) = (0, 0);
        var zoom = 0;
        var reset = false;

        if (key == Key.CursorLeft  || key == Key.H || key == Key.H.WithShift) panDx = -1;
        else if (key == Key.CursorRight || key == Key.L || key == Key.L.WithShift) panDx = 1;
        else if (key == Key.CursorUp    || key == Key.K || key == Key.K.WithShift) panDy = -1;
        else if (key == Key.CursorDown  || key == Key.J || key == Key.J.WithShift) panDy = 1;
        else if (IsZoomIn(key))  zoom = 1;
        else if (IsZoomOut(key)) zoom = -1;
        else if (key == Key.R || key == Key.R.WithShift) reset = true;
        else return;

        if (reset)
        {
            _viewport = new MapViewport(DefaultLat, DefaultLon, DefaultZoom, _viewport.PixelWidth, _viewport.PixelHeight);
        }
        else
        {
            if (panDx != 0 || panDy != 0)
            {
                var stepX = (int)Math.Max(1, Math.Round(_viewport.PixelWidth  * PanFraction)) * panDx;
                var stepY = (int)Math.Max(1, Math.Round(_viewport.PixelHeight * PanFraction)) * panDy;
                _viewport.Pan(stepX, stepY);
            }
            if (zoom > 0) _viewport.ZoomIn();
            else if (zoom < 0) _viewport.ZoomOut();
        }

        QueueRender();
        key.Handled = true;
    }

    // '+' arrives on US layouts as Shift+=; '-' is unshifted. Accept both forms plus PageUp /
    // PageDown so clients that swallow Shift still have a path.
    private static bool IsZoomIn(Key key) =>
        key == Key.PageUp || key.AsRune.Value == '+' || key.AsRune.Value == '=';

    private static bool IsZoomOut(Key key) =>
        key == Key.PageDown || key.AsRune.Value == '-' || key.AsRune.Value == '_';

    private void ResyncViewportSize()
    {
        var cols = Math.Max(20, Viewport.Width);
        var rows = Math.Max(5,  Viewport.Height - 5);
        // Vector mode: braille has 2 subpixels × 4 subpixels per cell. Raster (half-block):
        // 1 × 2. Same on-screen viewport, different sampling resolution.
        var (subX, subY) = _mode == RenderMode.Vector
            ? (BrailleCanvas.SubPixelsX, BrailleCanvas.SubPixelsY)
            : (1, 2);
        _viewport.Resize(cols * subX, rows * subY);
    }

    private void QueueRender()
    {
        CancelInFlight();
        var cts = new CancellationTokenSource();
        _renderCts = cts;
        var generation = ++_renderGeneration;
        var mode = _mode;

        _title.Text = FormatTitle("loading...");

        _ = Task.Run(async () =>
        {
            try
            {
                CellGrid? grid;
                if (mode == RenderMode.Vector)
                {
                    grid = await VectorMapRenderer.RenderAsync(_viewport, _vectorTiles, cts.Token).ConfigureAwait(false);
                    // Vector tiles can return null (network failure on the TileJSON probe);
                    // fall back to raster so the screen still shows something usable.
                    if (grid is null && !cts.IsCancellationRequested)
                    {
                        _logger.LogInformation("Vector render returned null at z={Zoom} — falling back to raster", _viewport.Zoom);
                        var rasterViewport = new MapViewport(_viewport.CenterLat, _viewport.CenterLon, _viewport.Zoom, _viewport.PixelWidth / 2, _viewport.PixelHeight / 2);
                        grid = await MapRenderer.RenderAsync(rasterViewport, _rasterTiles, cts.Token).ConfigureAwait(false);
                    }
                }
                else
                {
                    grid = await MapRenderer.RenderAsync(_viewport, _rasterTiles, cts.Token).ConfigureAwait(false);
                }

                if (cts.IsCancellationRequested) return;

                _app.Invoke(() =>
                {
                    if (generation != _renderGeneration) return;
                    if (grid is not null) _artView.Grid = grid;
                    _title.Text = FormatTitle(grid is null ? "no tiles" : "ready");
                });
            }
            catch (OperationCanceledException) { }
            catch (Exception ex)
            {
                _logger.LogWarning(ex, "Map render failed at z={Zoom} lat={Lat} lon={Lon} mode={Mode}",
                    _viewport.Zoom, _viewport.CenterLat, _viewport.CenterLon, mode);
                _app.Invoke(() =>
                {
                    if (generation != _renderGeneration) return;
                    _title.Text = FormatTitle("render error");
                });
            }
        });
    }

    private string FormatTitle(string state)
    {
        var modeTag = _mode == RenderMode.Vector ? "vec" : "ras";
        return $"map[{modeTag}] — z{_viewport.Zoom}  {_viewport.CenterLat,7:F4}°, {_viewport.CenterLon,8:F4}°  [{state}]";
    }

    private static string HintFor(RenderMode mode) => mode switch
    {
        RenderMode.Vector => "[←↑↓→ / hjkl] pan  [+/-] zoom  [r] reset  [v] raster  [Esc/Q] back",
        _                 => "[←↑↓→ / hjkl] pan  [+/-] zoom  [r] reset  [v] vector  [Esc/Q] back",
    };

    private void CancelInFlight()
    {
        var cts = _renderCts;
        if (cts is null) return;
        try { cts.Cancel(); } catch { /* already disposed */ }
        cts.Dispose();
        _renderCts = null;
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing) CancelInFlight();
        base.Dispose(disposing);
    }
}
