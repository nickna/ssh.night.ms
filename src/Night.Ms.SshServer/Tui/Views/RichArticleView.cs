using System.Text;
using Night.Ms.SshServer.Reader;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
using Mouse = Terminal.Gui.Input.Mouse;

namespace Night.Ms.SshServer.Tui.Views;

// Custom Terminal.Gui v2 View that renders an article block stream with per-run color and
// styling — headings, paragraphs, code, blockquotes, lists. Handles its own word-wrap
// (re-running on width change) and vertical scrolling. Bubbles non-scroll keys up so the
// hosting screen still receives Esc/Q/L/O.
//
// Centering: when the viewport is wider than MaxContentWidth, content is left-padded so
// the column hovers in the middle of the screen (max ~90 cols, the readable range).
// Re-layout is keyed on the *content* width, not the viewport width, so widening past 90
// just adds padding instead of triggering an expensive re-flow.
internal sealed class RichArticleView : View
{
    public int MaxContentWidth { get; set; } = 90;

    private IReadOnlyList<ArticleBlock> _blocks = Array.Empty<ArticleBlock>();
    private IReadOnlyList<BlockLayout.RenderedLine> _lines = Array.Empty<BlockLayout.RenderedLine>();
    private int _layoutWidth = -1;
    private int _topLine;
    private Func<Uri, CellGrid?> _imageResolver = _ => null;

    public RichArticleView()
    {
        CanFocus = true;
    }

    // Fired when the user clicks (or otherwise activates) inline-link text. The argument is
    // the 1-based LinkIndex into the parent article's Links list. Touch-screen SSH clients
    // (Termius, Blink, etc.) translate taps into LeftButtonClicked events at the tap
    // coordinates, so this also covers "tap a link to open it" on iPad/Android.
    public event EventHandler<int>? LinkActivated;

    public IReadOnlyList<ArticleBlock> Blocks
    {
        get => _blocks;
        set
        {
            _blocks = value ?? Array.Empty<ArticleBlock>();
            _layoutWidth = -1;
            _topLine = 0;
            SetNeedsDraw();
        }
    }

    // Resolver consulted at layout time for each ImageBlock — returns the rendered CellGrid
    // when the image has been fetched and rendered, or null while the fetch is still in
    // flight (or has failed). Setting this triggers a re-layout so that arriving images
    // replace their placeholder rows.
    public Func<Uri, CellGrid?> ImageResolver
    {
        get => _imageResolver;
        set
        {
            _imageResolver = value ?? (_ => null);
            _layoutWidth = -1;
            SetNeedsDraw();
        }
    }

    // Force re-layout from the outside without changing Blocks/ImageResolver — used when an
    // image arrives and the resolver's underlying state has changed but the delegate itself
    // is the same instance.
    public void InvalidateLayout()
    {
        _layoutWidth = -1;
        SetNeedsDraw();
    }

    public int LineCount => _lines.Count;

    public int VisibleHeight => Math.Max(0, Viewport.Height);

    private int MaxTop => Math.Max(0, _lines.Count - VisibleHeight);

    private int ContentWidth => Math.Max(1, Math.Min(Viewport.Width, MaxContentWidth));

    private int LeftPadding => Math.Max(0, (Viewport.Width - ContentWidth) / 2);

    public void ScrollBy(int delta)
    {
        SetTop(_topLine + delta);
    }

    public void ScrollToTop() => SetTop(0);
    public void ScrollToBottom() => SetTop(MaxTop);
    public void ScrollPage(int direction) => SetTop(_topLine + direction * Math.Max(1, VisibleHeight - 1));

    private void SetTop(int newTop)
    {
        var clamped = Math.Max(0, Math.Min(newTop, MaxTop));
        if (clamped == _topLine) return;
        _topLine = clamped;
        SetNeedsDraw();
    }

    protected override bool OnKeyDown(Key key)
    {
        if (key == Key.CursorUp)            { ScrollBy(-1); return true; }
        if (key == Key.CursorDown)          { ScrollBy(+1); return true; }
        if (key == Key.PageUp)              { ScrollPage(-1); return true; }
        if (key == Key.PageDown)            { ScrollPage(+1); return true; }
        if (key == Key.Space)               { ScrollPage(+1); return true; }
        if (key == Key.Home)                { ScrollToTop(); return true; }
        if (key == Key.End)                 { ScrollToBottom(); return true; }
        if (key == Key.G)                   { ScrollToTop(); return true; }
        if (key == Key.G.WithShift)         { ScrollToBottom(); return true; }
        if (key == Key.J)                   { ScrollBy(+1); return true; }
        if (key == Key.K)                   { ScrollBy(-1); return true; }
        return base.OnKeyDown(key);
    }

    // Touch-screen SSH clients (Termius, Blink, JuiceSSH, etc.) translate two-finger
    // pan / scroll-wheel gestures into MouseFlags.WheeledUp / WheeledDown events at the
    // pointer's view-relative position, so wiring the wheel here also covers swipe-to-
    // scroll on tablets and phones — there's no separate touch event type to handle.
    protected override bool OnMouseEvent(Mouse mouse)
    {
        if (mouse.Flags.HasFlag(MouseFlags.WheeledUp))    { ScrollBy(-WheelScrollLines); return true; }
        if (mouse.Flags.HasFlag(MouseFlags.WheeledDown))  { ScrollBy(+WheelScrollLines); return true; }

        if (mouse.Flags.HasFlag(MouseFlags.LeftButtonClicked) && mouse.Position is { } pos)
        {
            var linkIndex = HitTestLink(pos.X, pos.Y);
            if (linkIndex > 0)
            {
                LinkActivated?.Invoke(this, linkIndex);
                return true;
            }
        }

        return base.OnMouseEvent(mouse);
    }

    private const int WheelScrollLines = 3;

    private int HitTestLink(int viewX, int viewY)
    {
        if (viewY < 0 || viewY >= VisibleHeight) return 0;
        var lineIdx = viewY + _topLine;
        if (lineIdx < 0 || lineIdx >= _lines.Count) return 0;

        // Image rows are not link-bearing — skip hit-testing on them.
        if (_lines[lineIdx] is not BlockLayout.TextLine textLine) return 0;

        var col = viewX - LeftPadding;
        if (col < 0) return 0;

        var cursor = 0;
        foreach (var run in textLine.Runs)
        {
            var width = run.Text.Length;
            if (col < cursor + width)
            {
                return run.LinkIndex;
            }
            cursor += width;
        }
        return 0;
    }

    protected override bool OnDrawingContent(DrawContext? context)
    {
        var width = ContentWidth;
        var height = Viewport.Height;
        if (width <= 0 || height <= 0) return true;

        if (width != _layoutWidth)
        {
            _lines = BlockLayout.Layout(_blocks, width, _imageResolver);
            _layoutWidth = width;
            if (_topLine > MaxTop) _topLine = MaxTop;
        }

        // Paint background once with the body attribute so empty lines don't inherit
        // whatever was in the buffer from the previous screen.
        SetAttribute(ArticlePalette.Body);
        for (var y = 0; y < height; y++)
        {
            for (var x = 0; x < Viewport.Width; x++)
                AddRune(x, y, (Rune)' ');
        }

        var pad = LeftPadding;
        for (var row = 0; row < height && row + _topLine < _lines.Count; row++)
        {
            switch (_lines[row + _topLine])
            {
                case BlockLayout.TextLine text:
                    PaintTextLine(text, row, pad);
                    break;
                case BlockLayout.ImageRowLine image:
                    PaintImageRow(image, row, pad);
                    break;
            }
        }
        return true;
    }

    private void PaintTextLine(BlockLayout.TextLine line, int row, int pad)
    {
        var col = pad;
        foreach (var run in line.Runs)
        {
            SetAttribute(StyleToAttribute(run.Style));
            foreach (var rune in run.Text.EnumerateRunes())
            {
                if (col >= Viewport.Width) break;
                AddRune(col, row, rune);
                col += 1;
            }
            if (col >= Viewport.Width) break;
        }
    }

    private void PaintImageRow(BlockLayout.ImageRowLine line, int row, int pad)
    {
        // Center the image inside the content column when it's narrower than the body. A
        // 31-cell album thumbnail in an 80-cell body sits ~24 cells from the left, which
        // reads more "figure" than "left-flushed wall." Wider-than-body images skip the
        // centering and rely on the per-row clip in BlockLayout.LayoutImageBlock.
        var imageWidth = line.Cells.Count;
        var contentExtra = Math.Max(0, (ContentWidth - imageWidth) / 2);
        var col = pad + contentExtra;
        foreach (var cell in line.Cells)
        {
            if (col >= Viewport.Width) break;
            SetAttribute(new Attribute(
                new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B),
                new Color(cell.Background.R, cell.Background.G, cell.Background.B)));
            AddRune(col, row, cell.Glyph);
            col += 1;
        }
    }

    // Style-to-attribute mapping. Heading wins over everything (whole heading line is
    // bright-yellow bold even if a child <strong> set Bold). Quote applies dimming on top
    // of whatever else the run carries — so a Bold link inside a blockquote comes through
    // as cyan-on-black-underlined, but the gutter and unstyled body text dim. Inline
    // emphasis (Bold) and inline code share the body line color so they don't shout.
    // Italic composes additively on top of any base — SGR 3, supported since PuTTY 0.71.
    private static Attribute StyleToAttribute(RunStyle style)
    {
        var baseAttr = ComputeBaseAttribute(style);
        if (!style.HasFlag(RunStyle.Italic)) return baseAttr;
        return new Attribute(baseAttr.Foreground, baseAttr.Background, baseAttr.Style | TextStyle.Italic);
    }

    private static Attribute ComputeBaseAttribute(RunStyle style)
    {
        if (style.HasFlag(RunStyle.Heading)) return ArticlePalette.Heading;
        if (style.HasFlag(RunStyle.Link))    return ArticlePalette.Link;
        if (style.HasFlag(RunStyle.Code))    return ArticlePalette.Code;
        if (style.HasFlag(RunStyle.Bold))    return ArticlePalette.Strong;
        if (style.HasFlag(RunStyle.Quote))   return ArticlePalette.Quote;
        return ArticlePalette.Body;
    }
}

// Per-style attributes for article rendering. Composed from BbsTheme's anchor colors so a
// palette tweak (e.g. accent colors) flows through both chrome and article body.
internal static class ArticlePalette
{
    public static readonly Attribute Heading = new(BbsTheme.Header,     BbsTheme.Bg, TextStyle.Bold);
    public static readonly Attribute Link    = new(BbsTheme.Accent,     BbsTheme.Bg, TextStyle.Underline);
    public static readonly Attribute Code    = new(BbsTheme.Success,    BbsTheme.Bg);
    public static readonly Attribute Strong  = new(BbsTheme.BodyBright, BbsTheme.Bg, TextStyle.Bold);
    public static readonly Attribute Quote   = new(BbsTheme.Faint,      BbsTheme.Bg);
    public static readonly Attribute Body    = new(BbsTheme.Body,       BbsTheme.Bg);
}
