using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
using Cell = Night.Ms.SshServer.Tui.Art.Cell;
using Mouse = Terminal.Gui.Input.Mouse;

namespace Night.Ms.SshServer.Tui.Views;

// Scrollable chat log with per-run color/style attributes. The screen holds a list of
// entries (one per posted message or system notice); the view word-wraps each entry to the
// current viewport width and renders rows from the back so the newest entry sits at the
// bottom.
//
// Entries are addressable by MessageId for in-place updates (edits, deletes, reactions
// arriving asynchronously). When an entry's contents or reactions change, the layout is
// marked dirty and rebuilt on the next paint — cheap at chat scale (~100s of entries) and
// avoids partial-update bookkeeping.
internal sealed class ChatLogView : View
{
    // Cap kept loose — at 80 cols, 5000 logical entries × ~3 wrapped rows = ~15000 rendered
    // rows, ~120KB of segment structs. Cheap. The hard cap exists so a runaway publisher
    // can't OOM the session.
    private const int MaxEntries = 5000;

    private readonly List<Entry> _entries = new();
    private readonly List<DisplayRow> _displayRows = new();
    // O(1) message-id → entry lookup for in-place mutations (edits / reactions / images).
    // The previous List.FindIndex scan was O(N) per chat event; at high event rates against
    // a near-full log that scan added up to noticeable per-tick CPU. Only messages with a
    // non-null id are tracked (system notices have null ids and don't need lookup).
    private readonly Dictionary<long, Entry> _entryById = new();
    private int _layoutWidth = -1;
    private int _topRow;
    private bool _stickToBottom = true;

    public ChatLogView()
    {
        CanFocus = false;
    }

    public int EntryCount => _entries.Count;

    public void Append(ChatLine line, long? messageId = null)
    {
        var entry = new Entry { MessageId = messageId, Line = line };
        _entries.Add(entry);
        if (messageId is long mid) _entryById[mid] = entry;
        if (_entries.Count > MaxEntries)
        {
            var trim = _entries.Count - MaxEntries;
            for (var i = 0; i < trim; i++)
            {
                if (_entries[i].MessageId is long tid) _entryById.Remove(tid);
            }
            _entries.RemoveRange(0, trim);
        }
        InvalidateLayout();
    }

    // Replace an entry's line in place (used for edits/deletes that re-render the message
    // through the standard MessageRenderer). Returns false if the message isn't on screen.
    public bool TryReplace(long messageId, ChatLine newLine)
    {
        if (!_entryById.TryGetValue(messageId, out var entry)) return false;
        entry.Line = newLine;
        entry.CachedRows = null;
        InvalidateLayout();
        return true;
    }

    // Attach (or clear with an empty list) a reactions summary that paints as a single
    // footer row under the message. Empty list = no footer.
    public bool TrySetReactions(long messageId, IReadOnlyList<ReactionSummary> reactions)
    {
        if (!_entryById.TryGetValue(messageId, out var entry)) return false;
        entry.Reactions = reactions;
        entry.CachedRows = null;
        InvalidateLayout();
        return true;
    }

    // Attach a rendered image (half-block CellGrid) to a message. Multiple calls for the
    // same message accumulate — useful for messages containing several image URLs. The
    // image is painted as a block between the message body and any reactions footer.
    public bool TryAddImage(long messageId, CellGrid grid)
    {
        if (!_entryById.TryGetValue(messageId, out var entry)) return false;
        var images = entry.Images.ToList();
        images.Add(grid);
        entry.Images = images;
        entry.CachedRows = null;
        InvalidateLayout();
        return true;
    }

    // Wipe attached images for a message. Used when a message is deleted (tombstoned)
    // so the image rows don't survive past the body that referenced them.
    public bool TryClearImages(long messageId)
    {
        if (!_entryById.TryGetValue(messageId, out var entry)) return false;
        if (entry.Images.Count == 0) return false;
        entry.Images = Array.Empty<CellGrid>();
        entry.CachedRows = null;
        InvalidateLayout();
        return true;
    }

    public void Clear()
    {
        _entries.Clear();
        _entryById.Clear();
        _displayRows.Clear();
        _layoutWidth = -1;
        _topRow = 0;
        _stickToBottom = true;
        SetNeedsDraw();
    }

    // Scrollback API (the host screen calls these from its key handlers; we don't own focus
    // because the input field does).
    public void ScrollBy(int delta) => SetTop(_topRow + delta);
    public void ScrollPage(int direction) => SetTop(_topRow + direction * Math.Max(1, VisibleHeight - 1));
    public void ScrollToTop() => SetTop(0);
    public void ScrollToBottom() => SetTop(MaxTop);

    protected override bool OnMouseEvent(Mouse mouse)
    {
        if (mouse.Flags.HasFlag(MouseFlags.WheeledUp))   { ScrollBy(-3); return true; }
        if (mouse.Flags.HasFlag(MouseFlags.WheeledDown)) { ScrollBy(+3); return true; }
        return base.OnMouseEvent(mouse);
    }

    private int VisibleHeight => Math.Max(0, Viewport.Height);
    private int MaxTop => Math.Max(0, _displayRows.Count - VisibleHeight);

    private void InvalidateLayout()
    {
        _layoutWidth = -1;
        if (_stickToBottom) ScrollToBottom();
        SetNeedsDraw();
    }

    private void SetTop(int newTop)
    {
        var clamped = Math.Max(0, Math.Min(newTop, MaxTop));
        if (clamped == _topRow) return;
        _topRow = clamped;
        _stickToBottom = clamped >= MaxTop;
        SetNeedsDraw();
    }

    protected override bool OnDrawingContent(DrawContext? context)
    {
        var width = Viewport.Width;
        var height = Viewport.Height;
        if (width <= 0 || height <= 0) return true;

        if (width != _layoutWidth)
        {
            Relayout(width);
        }

        if (_stickToBottom) _topRow = MaxTop;

        SetAttribute(Default);
        for (var y = 0; y < height; y++)
            for (var x = 0; x < width; x++)
                AddRune(x, y, (Rune)' ');

        for (var row = 0; row < height; row++)
        {
            var rowIdx = row + _topRow;
            if (rowIdx >= _displayRows.Count) break;
            switch (_displayRows[rowIdx])
            {
                case TextDisplayRow text: PaintTextRow(text, row, width); break;
                case ImageDisplayRow img:  PaintImageRow(img, row, width); break;
            }
        }
        return true;
    }

    private void PaintTextRow(TextDisplayRow row, int y, int width)
    {
        var col = 0;
        foreach (var seg in row.Segments)
        {
            SetAttribute(ToAttribute(seg));
            foreach (var rune in seg.Text.EnumerateRunes())
            {
                if (col >= width) return;
                AddRune(col, y, rune);
                col += Math.Max(1, RuneWidths.Of(rune));
            }
        }
    }

    // Image rows carry a per-cell (fg, bg) pair from the half-block renderer; paint each
    // cell with its own attribute so background colors are honored (unlike text rows which
    // always assume black background).
    private void PaintImageRow(ImageDisplayRow row, int y, int width)
    {
        for (var col = 0; col < row.Cells.Count && col < width; col++)
        {
            var cell = row.Cells[col];
            var fg = new Color(cell.Foreground.R, cell.Foreground.G, cell.Foreground.B);
            var bg = new Color(cell.Background.R, cell.Background.G, cell.Background.B);
            var style = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
            SetAttribute(new Attribute(fg, bg, style));
            AddRune(col, y, cell.Glyph);
        }
    }

    // Stitches per-entry cached display rows into the flat _displayRows list. Each entry
    // owns its own pre-wrapped DisplayRow[] keyed by width; when one entry changes
    // (TryReplace / TrySetReactions / TryAddImage / TryClearImages) only its cache is
    // invalidated, so subsequent Relayout passes re-wrap exactly one entry instead of all N.
    // The full-list rebuild only does heavy word-wrap work when an entry's cache is stale.
    private void Relayout(int width)
    {
        _displayRows.Clear();
        foreach (var entry in _entries)
        {
            var rows = entry.CachedRows;
            if (rows is null || entry.CachedWidth != width)
            {
                rows = BuildEntryRows(entry, width);
                entry.CachedRows = rows;
                entry.CachedWidth = width;
            }
            // Manual loop (no LINQ) keeps the per-frame stitch allocation-free.
            for (var i = 0; i < rows.Count; i++) _displayRows.Add(rows[i]);
        }
        _layoutWidth = width;
        if (_topRow > MaxTop) _topRow = MaxTop;
    }

    private List<DisplayRow> BuildEntryRows(Entry entry, int width)
    {
        var rows = new List<DisplayRow>();
        WrapInto(rows, entry.Line, width);
        // Image rows render after the message body and before any reactions. One display
        // row per source CellGrid row; widths > viewport clip rather than wrap.
        foreach (var grid in entry.Images)
        {
            for (var y = 0; y < grid.Height; y++)
            {
                var rowWidth = Math.Min(grid.Width, width);
                var cells = new Cell[rowWidth];
                for (var x = 0; x < rowWidth; x++) cells[x] = grid[x, y];
                rows.Add(new ImageDisplayRow(cells));
            }
        }
        if (entry.Reactions.Count > 0)
        {
            rows.Add(BuildReactionRow(entry.Reactions, width));
        }
        return rows;
    }

    // Renders the reaction footer as one row: `  👍 3  ❤ 1`. Reactions the current user
    // contributed to (ByMe) paint bold so unreacting feels reversible. Two-space indent
    // mirrors the wrap-continuation indent used by message bodies.
    private static DisplayRow BuildReactionRow(IReadOnlyList<ReactionSummary> reactions, int width)
    {
        var segments = new List<RunSegment>();
        var col = 0;
        var indent = "  ";
        segments.Add(new RunSegment(indent, ArtColor.DefaultForeground, ArtStyle.None));
        col += indent.Length;

        var sep = "  ";
        var first = true;
        foreach (var r in reactions)
        {
            if (!first)
            {
                segments.Add(new RunSegment(sep, ArtColor.DefaultForeground, ArtStyle.None));
                col += sep.Length;
            }
            first = false;
            var emojiText = r.Emoji + " " + r.Count.ToString();
            var emojiWidth = DisplayLengthFor(emojiText);
            if (col + emojiWidth > width) break; // truncate rather than wrap reaction footer
            var style = r.ByMe ? ArtStyle.Bold : ArtStyle.None;
            var color = r.ByMe ? ChatPalette.ReactionByMe : ChatPalette.ReactionByOther;
            segments.Add(new RunSegment(emojiText, color, style));
            col += emojiWidth;
        }

        return new TextDisplayRow(segments.ToArray());
    }

    private static void WrapInto(List<DisplayRow> output, ChatLine line, int width)
    {
        var current = new List<RunSegment>();
        var currentWidth = 0;
        var continuationIndent = "  ";

        void Flush()
        {
            output.Add(new TextDisplayRow(current.ToArray()));
            current = new List<RunSegment>();
            currentWidth = 0;
        }

        void EmitWord(string word, ArtColor fg, ArtStyle style)
        {
            while (DisplayLengthFor(word) > width)
            {
                var sliceEnd = TruncateToWidth(word, width - currentWidth);
                if (sliceEnd == 0)
                {
                    Flush();
                    if (current.Count == 0)
                    {
                        current.Add(new RunSegment(continuationIndent, ArtColor.DefaultForeground, ArtStyle.None));
                        currentWidth = continuationIndent.Length;
                    }
                    sliceEnd = TruncateToWidth(word, width - currentWidth);
                    if (sliceEnd == 0) sliceEnd = 1;
                }
                current.Add(new RunSegment(word[..sliceEnd], fg, style));
                Flush();
                word = word[sliceEnd..];
            }

            var needed = DisplayLengthFor(word);
            if (currentWidth + needed > width && current.Count > 0)
            {
                Flush();
                current.Add(new RunSegment(continuationIndent, ArtColor.DefaultForeground, ArtStyle.None));
                currentWidth = continuationIndent.Length;
            }
            current.Add(new RunSegment(word, fg, style));
            currentWidth += needed;
        }

        foreach (var run in line.Runs)
        {
            var text = run.Text;
            var i = 0;
            while (i < text.Length)
            {
                // Hard line break — flush the current row (even if empty, so consecutive
                // \n produce a blank line) and drop the newline itself.
                if (text[i] == '\n')
                {
                    Flush();
                    i++;
                    continue;
                }

                if (text[i] == ' ')
                {
                    var space = i;
                    while (i < text.Length && text[i] == ' ') i++;
                    var run2 = new RunSegment(text[space..i], run.Foreground, run.Style);
                    if (currentWidth + (i - space) > width && current.Count > 0) Flush();
                    if (currentWidth + (i - space) <= width)
                    {
                        current.Add(run2);
                        currentWidth += i - space;
                    }
                    continue;
                }

                var start = i;
                while (i < text.Length && text[i] != ' ' && text[i] != '\n') i++;
                EmitWord(text[start..i], run.Foreground, run.Style);
            }
        }

        Flush();
    }

    private static int DisplayLengthFor(string s)
    {
        var total = 0;
        foreach (var r in s.EnumerateRunes())
            total += Math.Max(1, RuneWidths.Of(r));
        return total;
    }

    private static int TruncateToWidth(string s, int maxColumns)
    {
        if (maxColumns <= 0) return 0;
        var col = 0;
        var idx = 0;
        foreach (var r in s.EnumerateRunes())
        {
            var w = Math.Max(1, RuneWidths.Of(r));
            if (col + w > maxColumns) return idx;
            col += w;
            idx += r.Utf16SequenceLength;
        }
        return idx;
    }

    private static Attribute ToAttribute(RunSegment seg)
    {
        var fg = new Color(seg.Foreground.R, seg.Foreground.G, seg.Foreground.B);
        var bg = new Color(0, 0, 0);
        var ts = TextStyle.None;
        if (seg.Style.HasFlag(ArtStyle.Bold))      ts |= TextStyle.Bold;
        if (seg.Style.HasFlag(ArtStyle.Italic))    ts |= TextStyle.Italic;
        if (seg.Style.HasFlag(ArtStyle.Underline)) ts |= TextStyle.Underline;
        return new Attribute(fg, bg, ts);
    }

    private static readonly Attribute Default = new(
        new Color(ArtColor.DefaultForeground.R, ArtColor.DefaultForeground.G, ArtColor.DefaultForeground.B),
        new Color(0, 0, 0));

    // Owned by the view (not exposed to callers) so future fields can be added without
    // breaking the public surface. Use Append / TryReplace / TrySetReactions to mutate.
    private sealed class Entry
    {
        public long? MessageId;
        public required ChatLine Line { get; set; }
        public IReadOnlyList<ReactionSummary> Reactions { get; set; } = Array.Empty<ReactionSummary>();
        // Inline images attached to this message. Each grid renders as a contiguous block
        // of ImageDisplayRows between the message body and the reactions footer.
        public IReadOnlyList<CellGrid> Images { get; set; } = Array.Empty<CellGrid>();
        // Pre-wrapped display rows for this entry at CachedWidth. Set to null whenever the
        // entry's content changes (line / reactions / images); Relayout rebuilds it on next
        // pass. Keeping the cache on the entry means a single chat event only re-wraps the
        // one affected entry instead of all N entries in the log.
        public List<DisplayRow>? CachedRows;
        public int CachedWidth;
    }

    private readonly record struct RunSegment(string Text, ArtColor Foreground, ArtStyle Style);

    // Display rows are tagged so the painter can switch between text (default-black bg) and
    // image rows (per-cell bg from the half-block renderer).
    private abstract record DisplayRow;
    private sealed record TextDisplayRow(IReadOnlyList<RunSegment> Segments) : DisplayRow;
    private sealed record ImageDisplayRow(IReadOnlyList<Cell> Cells) : DisplayRow;
}
