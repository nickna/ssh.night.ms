using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Chat;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;
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
        _entries.Add(new Entry { MessageId = messageId, Line = line });
        if (_entries.Count > MaxEntries)
        {
            _entries.RemoveRange(0, _entries.Count - MaxEntries);
        }
        InvalidateLayout();
    }

    // Replace an entry's line in place (used for edits/deletes that re-render the message
    // through the standard MessageRenderer). Returns false if the message isn't on screen.
    public bool TryReplace(long messageId, ChatLine newLine)
    {
        var idx = _entries.FindIndex(e => e.MessageId == messageId);
        if (idx < 0) return false;
        _entries[idx].Line = newLine;
        InvalidateLayout();
        return true;
    }

    // Attach (or clear with an empty list) a reactions summary that paints as a single
    // footer row under the message. Empty list = no footer.
    public bool TrySetReactions(long messageId, IReadOnlyList<ReactionSummary> reactions)
    {
        var idx = _entries.FindIndex(e => e.MessageId == messageId);
        if (idx < 0) return false;
        _entries[idx].Reactions = reactions;
        InvalidateLayout();
        return true;
    }

    public void Clear()
    {
        _entries.Clear();
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
            PaintRow(_displayRows[rowIdx], row, width);
        }
        return true;
    }

    private void PaintRow(DisplayRow row, int y, int width)
    {
        var col = 0;
        foreach (var seg in row.Segments)
        {
            SetAttribute(ToAttribute(seg));
            foreach (var rune in seg.Text.EnumerateRunes())
            {
                if (col >= width) return;
                AddRune(col, y, rune);
                col += Math.Max(1, RuneWidth(rune));
            }
        }
    }

    private static int RuneWidth(Rune r)
    {
        var v = r.Value;
        if (v < 0x300) return 1;
        if (v >= 0x1F300 && v <= 0x1FAFF) return 2;
        if (v >= 0x2600 && v <= 0x27BF)   return 2;
        if (v >= 0x3000 && v <= 0x9FFF)   return 2;
        if (v >= 0xFE30 && v <= 0xFE4F)   return 2;
        if (v >= 0xFF00 && v <= 0xFF60)   return 2;
        return 1;
    }

    private void Relayout(int width)
    {
        _displayRows.Clear();
        foreach (var entry in _entries)
        {
            WrapAndAppend(entry.Line, width);
            if (entry.Reactions.Count > 0)
            {
                AppendReactionRow(entry.Reactions, width);
            }
        }
        _layoutWidth = width;
        if (_topRow > MaxTop) _topRow = MaxTop;
    }

    // Renders the reaction footer as one row: `  👍 3  ❤ 1`. Reactions the current user
    // contributed to (ByMe) paint bold so unreacting feels reversible. Two-space indent
    // mirrors the wrap-continuation indent used by message bodies.
    private void AppendReactionRow(IReadOnlyList<ReactionSummary> reactions, int width)
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
            var color = r.ByMe ? new ArtColor(0xFF, 0xD7, 0x00) : new ArtColor(0xB0, 0xB0, 0xB0);
            segments.Add(new RunSegment(emojiText, color, style));
            col += emojiWidth;
        }

        _displayRows.Add(new DisplayRow(segments.ToArray()));
    }

    private void WrapAndAppend(ChatLine line, int width)
    {
        var current = new List<RunSegment>();
        var currentWidth = 0;
        var continuationIndent = "  ";

        void Flush()
        {
            _displayRows.Add(new DisplayRow(current.ToArray()));
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
                while (i < text.Length && text[i] != ' ') i++;
                EmitWord(text[start..i], run.Foreground, run.Style);
            }
        }

        Flush();
    }

    private static int DisplayLengthFor(string s)
    {
        var total = 0;
        foreach (var r in s.EnumerateRunes())
            total += Math.Max(1, RuneWidth(r));
        return total;
    }

    private static int TruncateToWidth(string s, int maxColumns)
    {
        if (maxColumns <= 0) return 0;
        var col = 0;
        var idx = 0;
        foreach (var r in s.EnumerateRunes())
        {
            var w = Math.Max(1, RuneWidth(r));
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
    }

    private readonly record struct RunSegment(string Text, ArtColor Foreground, ArtStyle Style);
    private readonly record struct DisplayRow(IReadOnlyList<RunSegment> Segments);
}
