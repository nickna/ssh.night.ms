using System.Collections.Concurrent;
using Night.Ms.SshServer.Tui.Art;
using Terminal.Gui.Drawing;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Views;

// Memoized (ArtColor fg, ArtStyle style) → Terminal.Gui Attribute lookup. The chat views
// (ChatLogView text rows, ChatInputPreview, BbsChatStatusLine) used to allocate two Color
// structs and an Attribute every paint per text segment; with hundreds of segments per
// frame across many sessions, that was the dominant per-frame allocation. The palette is
// small and bounded (ChatPalette plus the per-handle colorizer), so a static cache hits
// 100% of the time after warmup.
//
// Image cells need a per-cell background, not the default — see ForCell below.
internal static class AttributeCache
{
    private static readonly ConcurrentDictionary<long, Attribute> _textCache = new();
    private static readonly ConcurrentDictionary<(long Key, byte BgR, byte BgG, byte BgB), Attribute> _cellCache = new();
    private static readonly Color DefaultBg = new(0, 0, 0);

    // Text runs always paint over the default black background. Key packs fg RGB + style into
    // a single long so the dictionary uses a fast value-typed comparer.
    public static Attribute For(ArtColor fg, ArtStyle style)
    {
        var key = MakeTextKey(fg, style);
        if (_textCache.TryGetValue(key, out var attr)) return attr;
        attr = Build(fg, DefaultBg, style);
        _textCache.TryAdd(key, attr);
        return attr;
    }

    // Image cells carry their own background (the bottom-pixel color from the half-block
    // renderer). Cache key includes bg so each cell-color combination lands once.
    public static Attribute ForCell(ArtColor fg, ArtColor bg, ArtStyle style)
    {
        var key = (MakeTextKey(fg, style), bg.R, bg.G, bg.B);
        if (_cellCache.TryGetValue(key, out var attr)) return attr;
        attr = Build(fg, new Color(bg.R, bg.G, bg.B), style);
        _cellCache.TryAdd(key, attr);
        return attr;
    }

    private static long MakeTextKey(ArtColor fg, ArtStyle style)
        => ((long)fg.R) | ((long)fg.G << 8) | ((long)fg.B << 16) | ((long)(byte)style << 24);

    private static Attribute Build(ArtColor fg, Color bg, ArtStyle style)
    {
        var ts = TextStyle.None;
        if (style.HasFlag(ArtStyle.Bold))      ts |= TextStyle.Bold;
        if (style.HasFlag(ArtStyle.Italic))    ts |= TextStyle.Italic;
        if (style.HasFlag(ArtStyle.Underline)) ts |= TextStyle.Underline;
        return new Attribute(new Color(fg.R, fg.G, fg.B), bg, ts);
    }
}
