using System.Collections.Concurrent;
using System.Collections.ObjectModel;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.Imaging;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.App;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Terminal.Gui.Views;

namespace Night.Ms.SshServer.Tui.Screens;

// Lynx-style reader for an arbitrary HTTP(S) URL. Calls IArticleReader on a background
// task, then renders the extracted title / byline / body in a custom RichArticleView that
// paints headings, code blocks, blockquotes, lists, inline bold and link runs with their
// own colors. Press L to flip into a links pane (anchors lifted from the article body, in
// DOM order so [N] references inline match the list); press Esc to flip back.
//
// Reusable beyond NewsScreen: any screen that wants to surface a URL for in-BBS reading
// can `app.Run(new ReaderScreen(app, services, user, uri))`. Returns no result.
public sealed class ReaderScreen : BbsWindow
{
    private readonly IServiceProvider _services;
    private readonly IApplication _app;
    private readonly User _user;
    private readonly Uri _url;

    private readonly Label _title;
    private readonly Label _meta;
    private readonly RichArticleView _body;
    private readonly ListView _linksView;
    private readonly Label _hint;

    private readonly ConcurrentDictionary<Uri, CellGrid> _imageCells = new();
    private ReaderArticle? _article;
    private bool _showingLinks;
    private ReadMode _mode = ReadMode.Reader;

    // Cap on cell-columns when rendering a fetched image. The actual render width is chosen
    // per-image from the source pixel width (see ChooseImageRenderCols) so a 250px album
    // thumbnail doesn't blow up to fill an 80-cell body — it lands at ~30 cells and reads
    // like a real thumbnail. The cap matches the typical reader-mode body column.
    private const int ImageRenderColsCap = 80;
    private const int ImageRenderColsFloor = 8;
    private const int ImageSourcePixelsPerCell = 8;
    private const int ImageFetchConcurrency = 4;

    public ReaderScreen(IApplication app, IServiceProvider services, User user, Uri url)
        : base(app, services, user)
    {
        _services = services;
        _app = app;
        _user = user;
        _url = url;

        Title = $"ssh.night.ms — reader — {Truncate(url.Host, 50)}";

        _title = new Label
        {
            X = 0,
            Y = 0,
            Width = Dim.Fill(),
            Text = "fetching...",
        };
        _title.SetScheme(BbsTheme.Header_);

        _meta = new Label
        {
            X = 0,
            Y = 1,
            Width = Dim.Fill(),
            Text = url.ToString(),
        };
        _meta.SetScheme(BbsTheme.Faint_);

        _body = new RichArticleView
        {
            X = 0,
            Y = 3,
            Width = Dim.Fill(),
            Height = Dim.Fill(2),
            ImageResolver = u => _imageCells.TryGetValue(u, out var g) ? g : null,
        };

        _linksView = new ListView
        {
            X = 0,
            Y = 3,
            Width = Dim.Fill(),
            Height = Dim.Fill(2),
            Visible = false,
        };

        _hint = new Label
        {
            X = 0,
            Y = Pos.AnchorEnd(2),
            Width = Dim.Fill(),
            Text = "[Esc] cancel",
        };
        _hint.SetScheme(BbsTheme.Hint);

        Add(_title, _meta, _body, _linksView, _hint);
        _body.SetFocus();

        // RichArticleView consumes scroll keys (arrows / PgUp/PgDn / g / G / Home/End / j / k)
        // and mouse wheel before they bubble. Click-on-link inside the body raises
        // LinkActivated; everything else lands here on the Window.
        _body.LinkActivated += (_, idx) => OpenLinkByIndex(idx);
        KeyDown += OnKey;
        _linksView.KeyDown += OnKey;

        LoadAsync().FireAndLog(_services, nameof(LoadAsync));
    }

    private void OnKey(object? _, Key key)
    {
        if (key == Key.Enter && _showingLinks)
        {
            OpenSelectedLink();
            key.Handled = true;
            return;
        }

        // Digit shortcut: 1-9 opens link N directly from the body view (skipping the
        // links pane). Articles with 10+ links still need the pane for the rest.
        if (!_showingLinks && TryDigit(key, out var n))
        {
            OpenLinkByIndex(n);
            key.Handled = true;
            return;
        }

        if (key == Key.Esc)
        {
            if (_showingLinks)
            {
                ShowBody();
                key.Handled = true;
                return;
            }
            _app.RequestStop();
            key.Handled = true;
            return;
        }

        if (key.Matches(Key.Q))
        {
            _app.RequestStop();
            key.Handled = true;
            return;
        }

        if (key.Matches(Key.L))
        {
            ToggleLinks();
            key.Handled = true;
            return;
        }

        if (key.Matches(Key.O))
        {
            _hint.Text = $"url: {_url}    [Esc] back";
            key.Handled = true;
            return;
        }

        if (key.Matches(Key.R))
        {
            _mode = _mode == ReadMode.Reader ? ReadMode.Raw : ReadMode.Reader;
            _title.Text = _mode == ReadMode.Raw ? "fetching (raw mode)..." : "fetching...";
            _title.SetScheme(BbsTheme.Header_);
            _meta.Text = _url.ToString();
            _body.Blocks = Array.Empty<ArticleBlock>();
            _imageCells.Clear();
            _article = null;
            if (_showingLinks) ShowBody();
            LoadAsync().FireAndLog(_services, nameof(LoadAsync));
            key.Handled = true;
            return;
        }
    }

    private void OpenSelectedLink()
    {
        var selected = _linksView.SelectedItem ?? -1;
        OpenLinkByIndex(selected + 1);
    }

    // Open the link at 1-based DOM-order index (matching the inline [N] references). Used
    // by Enter on the links pane, by digit shortcuts in the body, and by mouse / touch
    // clicks on link runs in the body. No-op if the index is out of range.
    private void OpenLinkByIndex(int oneBasedIndex)
    {
        if (_article is null) return;
        var idx = oneBasedIndex - 1;
        if (idx < 0 || idx >= _article.Links.Count) return;
        var link = _article.Links[idx];

        // Nested Application.Run — control returns to whichever pane was focused when the
        // inner ReaderScreen calls RequestStop. The user can pick another link, Esc back
        // to the article body, or Q back to whatever opened us (NewsScreen, or the parent
        // ReaderScreen if we're already nested).
        _app.Run(new ReaderScreen(_app, _services, _user, link.Url));

        // Reapply our hint after the inner screen closes — its Title/hint overwrote ours.
        SetNeedsDraw();
    }

    private static bool TryDigit(Key key, out int digit)
    {
        if (key == Key.D1) { digit = 1; return true; }
        if (key == Key.D2) { digit = 2; return true; }
        if (key == Key.D3) { digit = 3; return true; }
        if (key == Key.D4) { digit = 4; return true; }
        if (key == Key.D5) { digit = 5; return true; }
        if (key == Key.D6) { digit = 6; return true; }
        if (key == Key.D7) { digit = 7; return true; }
        if (key == Key.D8) { digit = 8; return true; }
        if (key == Key.D9) { digit = 9; return true; }
        digit = 0;
        return false;
    }

    private void ToggleLinks()
    {
        if (_showingLinks) ShowBody(); else ShowLinks();
    }

    private void ShowBody()
    {
        _showingLinks = false;
        _linksView.Visible = false;
        _body.Visible = true;
        _body.SetFocus();
        ApplyHint();
        SetNeedsDraw();
    }

    private void ShowLinks()
    {
        if (_article is null) return;
        _showingLinks = true;
        var lines = _article.Links.Count == 0
            ? new ObservableCollection<string>(new[] { "(no links found in this article)" })
            : new ObservableCollection<string>(_article.Links.Select((l, i) => FormatLink(i + 1, l)));
        _linksView.SetSource<string>(lines);
        _body.Visible = false;
        _linksView.Visible = true;
        _linksView.SetFocus();
        _hint.Text = $"links: {_article.Links.Count}    [Enter] open    [L/Esc] back to article    [Q] back";
        SetNeedsDraw();
    }

    private void ApplyHint()
    {
        if (_article is null)
        {
            _hint.Text = "[Esc] cancel";
            return;
        }
        var modeTag = _mode == ReadMode.Raw ? "[raw] " : string.Empty;
        var n = _article.Links.Count;
        if (n == 0)
        {
            _hint.Text = $"{modeTag}[Esc/Q] back    [R] toggle reader/raw    [O] show url    [↑/↓/PgUp/PgDn or wheel] scroll";
            return;
        }
        // 1-9 are direct shortcuts in the body view; the L pane is the only way to reach
        // [10] and beyond, so we mention it conditionally to avoid noise on short articles.
        var digitHint = n >= 10 ? "[1-9] open    [L] all links" : "[1-9] open    [L] links";
        _hint.Text = $"{modeTag}[Esc/Q] back    {digitHint}    [R] reader/raw    [O] show url    [↑/↓/PgUp/PgDn or wheel] scroll";
    }

    private async Task LoadAsync()
    {
        ReaderArticle? article = null;
        try
        {
            using var scope = _services.CreateScope();
            var reader = scope.ServiceProvider.GetRequiredService<IArticleReader>();
            article = await reader.ReadAsync(_url, _mode, Shutdown).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            return;
        }
        catch
        {
            // Reader implementation already swallows transport/parse errors; this catch is
            // belt-and-braces for an unexpected DI / scope failure.
        }

        if (Shutdown.IsCancellationRequested) return;

        _app.Invoke(() =>
        {
            _article = article;
            if (article is null)
            {
                _title.Text = "(couldn't extract a readable article)";
                _title.SetScheme(BbsTheme.Warning);
                _meta.Text = _url.ToString();
                _body.Blocks = new ArticleBlock[]
                {
                    new ParagraphBlock(new[]
                    {
                        new Run("Reader extraction returned no content. The page may be a SPA, "
                            + "media file, login wall, or otherwise non-article content."),
                    }),
                    new ParagraphBlock(new[]
                    {
                        new Run("Try opening the URL above in your local browser."),
                    }),
                };
                ApplyHint();
                return;
            }

            _title.Text = article.Title ?? _url.Host;
            _title.SetScheme(BbsTheme.Header_);
            _meta.Text = FormatMeta(article, _url);
            _body.Blocks = article.Blocks;
            ApplyHint();

            // Kick off parallel image fetches now that the article is on-screen with
            // placeholders. Each completion paints into _imageCells and triggers a re-layout.
            LoadImagesAsync(article.Blocks).FireAndLog(_services, nameof(LoadImagesAsync));
        });
    }

    private async Task LoadImagesAsync(IReadOnlyList<ArticleBlock> blocks)
    {
        var imageBlocks = CollectImageBlocks(blocks);
        if (imageBlocks.Count == 0) return;

        IImageFetcher fetcher;
        try
        {
            // Singleton service — resolve through a scope just to keep the DI pattern
            // consistent with the rest of the screen, even though the cache is process-wide.
            using var scope = _services.CreateScope();
            fetcher = scope.ServiceProvider.GetRequiredService<IImageFetcher>();
        }
        catch
        {
            return; // No image fetcher registered — leave placeholders as-is.
        }

        using var sem = new SemaphoreSlim(ImageFetchConcurrency, ImageFetchConcurrency);
        var distinct = imageBlocks
            .Select(b => b.Source)
            .Where(u => !_imageCells.ContainsKey(u))
            .Distinct()
            .ToList();

        var tasks = distinct.Select(async url =>
        {
            await sem.WaitAsync(Shutdown).ConfigureAwait(false);
            try
            {
                if (Shutdown.IsCancellationRequested) return;
                var image = await fetcher.FetchAsync(url, Shutdown).ConfigureAwait(false);
                if (image is null || Shutdown.IsCancellationRequested) return;

                // String roundtrip via SgrParser: the renderer emits ANSI (SGR + half-block
                // ▀) and the parser turns that into a CellGrid of (rune, fg, bg) cells. Less
                // efficient than direct cell emission but reuses code and keeps the imaging
                // library decoupled from the server's CellGrid type.
                var targetCols = ChooseImageRenderCols(image.Width);
                var ansi = HalfBlockRenderer.Render(image, targetCols, ColorDepth.Truecolor, DitherMode.None);
                var grid = SgrParser.Parse(ansi);
                _imageCells[url] = grid;

                _app.Invoke(() => _body.InvalidateLayout());
            }
            catch (OperationCanceledException)
            {
                // Screen disposed while we were in flight — drop quietly.
            }
            catch
            {
                // Any other failure: leave the placeholder in place. The fetcher already
                // logs at info level; a per-image swallow here keeps one bad URL from
                // tearing down the whole article view.
            }
            finally
            {
                sem.Release();
            }
        });

        try { await Task.WhenAll(tasks).ConfigureAwait(false); }
        catch { /* aggregated above */ }
    }

    // Choose how wide to render an image (in cell columns) given its source pixel width.
    // 8 source pixels per cell is a reasonable thumbnail ratio: a 250px image lands at
    // ~31 cells (which becomes ~16 cell rows after the half-block 2x vertical doubling),
    // a 1200px hero caps at the body width, and tiny icons floor at 8 cells so they
    // remain visible. Hard cap at the body column avoids hot-page jitter when a 4000px
    // image arrives.
    private static int ChooseImageRenderCols(int sourcePixelWidth)
    {
        var raw = sourcePixelWidth / ImageSourcePixelsPerCell;
        if (raw < ImageRenderColsFloor) raw = ImageRenderColsFloor;
        if (raw > ImageRenderColsCap) raw = ImageRenderColsCap;
        return raw;
    }

    private static List<ImageBlock> CollectImageBlocks(IReadOnlyList<ArticleBlock> blocks)
    {
        var result = new List<ImageBlock>();
        Walk(blocks);
        return result;

        void Walk(IReadOnlyList<ArticleBlock> bs)
        {
            foreach (var b in bs)
            {
                switch (b)
                {
                    case ImageBlock i:
                        result.Add(i);
                        break;
                    case BlockquoteBlock bq:
                        Walk(bq.Children);
                        break;
                }
            }
        }
    }

    private static string FormatMeta(ReaderArticle a, Uri url)
    {
        var parts = new List<string>(4);
        if (!string.IsNullOrEmpty(a.Byline)) parts.Add(a.Byline!);
        parts.Add(string.IsNullOrEmpty(a.SiteName) ? url.Host : a.SiteName!);
        if (a.PublishedAt is { } pub) parts.Add(pub.ToString("yyyy-MM-dd"));
        if (a.ReadingTimeMinutes is { } mins) parts.Add($"{mins} min read");
        return string.Join("  —  ", parts);
    }

    private static string FormatLink(int index, ReaderLink link)
    {
        var host = link.Url.Host;
        var text = link.Text;
        var prefix = $"[{index,2}] ";
        if (text.Length > 56) text = text[..53] + "...";
        return $"{prefix}{text}  ({host})";
    }

    private static string Truncate(string s, int max) =>
        s.Length <= max ? s : s[..(max - 1)] + "…";
}
