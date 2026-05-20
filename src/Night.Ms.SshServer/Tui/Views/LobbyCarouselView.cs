using System.Text;
using Night.Ms.SshServer.Tui.Art;
using Night.Ms.SshServer.Tui.Theme;
using Terminal.Gui.App;
using Terminal.Gui.Drawing;
using Terminal.Gui.Input;
using Terminal.Gui.ViewBase;
using Attribute = Terminal.Gui.Drawing.Attribute;

namespace Night.Ms.SshServer.Tui.Views;

// Centered horizontal carousel of destination cards. The selected entry is drawn larger with a
// double-line accent border at the visual center; unselected neighbours fan out in both
// directions, dimmed by RGB-blending their colors toward the screen background based on their
// distance from center. Moving between entries plays a slide+scale tween: the incoming card
// grows in place while neighbours slide one slot toward the edge. Rapid arrow input cancels
// the in-flight tween and retargets from the current interpolated state.
//
// The target type is generic so the same control drives the main lobby (TTarget =
// LobbyNavigation) and sub-hubs like Doors (TTarget = Action).
internal sealed class LobbyCarouselView<TTarget> : View where TTarget : notnull
{
    public sealed record Entry(string Label, Key Hotkey, TTarget Target, CellGrid Icon);

    public const int RowHeight = 7;

    private const int SelectedWidth = 20;
    private const int SelectedHeight = 7;
    private const int UnselectedWidth = 14;
    private const int UnselectedHeight = 5;
    private const int Gap = 2;

    // Maximum signed slot we ever bother rendering. With selected width 20 + gap 2 + 5 × (14+2)
    // per side we cover ~110 cols — enough for the widest reasonable terminal.
    private const int MaxSlot = 5;

    // Width threshold at which we flip from single-line/normal to double-line/bold border. Set
    // just under the midpoint between unselected (14) and selected (20) so the visual swap
    // lands while the card is still moving, hiding the discrete transition.
    private const int SelectedBorderWidth = 17;

    private static readonly TimeSpan AnimDuration = TimeSpan.FromMilliseconds(200);
    private static readonly TimeSpan FrameInterval = TimeSpan.FromMilliseconds(33);

    // Per-slot RGB dim alpha: 1.0 = full color, 0.0 = blended fully into BbsTheme.Bg. Indexed
    // by absolute slot distance. Slot 0 is the selected card.
    private static readonly double[] SlotAlpha = { 1.00, 0.72, 0.48, 0.30, 0.18, 0.12 };

    private readonly IReadOnlyList<Entry> _entries;
    private readonly IApplication _app;
    private int _index;

    // Tween state. _animTimerToken is null when idle.
    private object? _animTimerToken;
    private DateTime _animStartUtc;
    private Dictionary<int, CardGeom> _animFrom = new(16);
    private Dictionary<int, CardGeom> _animTo = new(16);

    // Reusable scratch buffers — refilled in place each frame instead of allocating new
    // collections. The paint path runs at ~30 FPS during animations, and the previous shape
    // built a fresh Dictionary + LINQ Union + OrderByDescending.ToList() per frame.
    // _geomBuf is non-readonly because RetargetTo swaps it with _animFrom to avoid
    // reallocating either dictionary on user input.
    private Dictionary<int, CardGeom> _geomBuf = new(16);
    private readonly List<KeyValuePair<int, CardGeom>> _sortBuf = new(16);
    private readonly List<int> _idxBuf = new(16);

    private struct CardGeom
    {
        public double LeftX;
        public double TopY;
        public double Width;
        public double Height;
        public double Alpha;
    }

    public LobbyCarouselView(IApplication app, IReadOnlyList<Entry> entries)
    {
        if (entries.Count == 0) throw new ArgumentException("at least one entry required", nameof(entries));
        _app = app;
        _entries = entries;
        CanFocus = true;
        Height = RowHeight;

        KeyDown += OnKey;
    }

    public int SelectedIndex => _index;
    public TTarget SelectedTarget => _entries[_index].Target;
    public IReadOnlyList<Entry> Entries => _entries;

    public event EventHandler<TTarget>? EntryActivated;
    public event EventHandler<int>? SelectionChanged;

    public void MoveLeft() => RetargetTo((_index - 1 + _entries.Count) % _entries.Count, direction: -1);

    public void MoveRight() => RetargetTo((_index + 1) % _entries.Count, direction: +1);

    public bool TrySelectByHotkey(Key key)
    {
        for (var i = 0; i < _entries.Count; i++)
        {
            var hk = _entries[i].Hotkey;
            if (key == hk || key == hk.WithShift)
            {
                JumpTo(i);
                return true;
            }
        }
        return false;
    }

    // Hotkey jumps pick the shorter direction around the wrap so the slide animation is the
    // shortest visible motion. Ties prefer rightward.
    private void JumpTo(int targetIndex)
    {
        if (targetIndex == _index) return;
        var n = _entries.Count;
        var rightDist = (targetIndex - _index + n) % n;
        var leftDist = (_index - targetIndex + n) % n;
        var direction = rightDist <= leftDist ? +1 : -1;
        RetargetTo(targetIndex, direction);
    }

    private void RetargetTo(int newIndex, int direction)
    {
        if (newIndex == _index && _animTimerToken is null) return;

        // Snapshot the current visual state as the new tween origin. If a tween is in flight,
        // this captures the half-way interpolated geometry; if idle, it's just the static
        // layout for the current _index. We write into _geomBuf and swap with _animFrom so
        // neither dictionary is reallocated.
        SnapshotCurrentGeomInto(_geomBuf);
        (_animFrom, _geomBuf) = (_geomBuf, _animFrom);
        _geomBuf.Clear();

        _index = newIndex;
        _animTo.Clear();
        BuildTargetGeomInto(_animTo);

        // Indices in the origin set that aren't in the target set keep their from-geom but
        // need a to-geom that slides them off-screen in the move direction.
        foreach (var kv in _animFrom)
        {
            if (_animTo.ContainsKey(kv.Key)) continue;
            _animTo[kv.Key] = OffscreenGeomMatching(kv.Value, direction);
        }
        // Conversely, indices in the target set that weren't in the origin set need a
        // from-geom that slides them in from the opposite edge. Collect keys first so we
        // don't mutate _animFrom while iterating _animTo.
        _idxBuf.Clear();
        foreach (var kv in _animTo)
        {
            if (!_animFrom.ContainsKey(kv.Key)) _idxBuf.Add(kv.Key);
        }
        for (var i = 0; i < _idxBuf.Count; i++)
        {
            var idx = _idxBuf[i];
            _animFrom[idx] = OffscreenGeomMatching(_animTo[idx], -direction);
        }

        _animStartUtc = DateTime.UtcNow;
        if (_animTimerToken is null)
        {
            _animTimerToken = _app.AddTimeout(FrameInterval, OnAnimTick);
        }

        SetNeedsDraw();
        SelectionChanged?.Invoke(this, _index);
    }

    private bool OnAnimTick()
    {
        var elapsed = DateTime.UtcNow - _animStartUtc;
        if (elapsed >= AnimDuration)
        {
            _animTimerToken = null;
            _animFrom.Clear();
            _animTo.Clear();
            SetNeedsDraw();
            return false;
        }
        SetNeedsDraw();
        return true;
    }

    private double Progress()
    {
        if (_animTimerToken is null) return 1.0;
        var t = (DateTime.UtcNow - _animStartUtc).TotalMilliseconds / AnimDuration.TotalMilliseconds;
        t = Math.Clamp(t, 0.0, 1.0);
        // Ease-out cubic.
        var inv = 1.0 - t;
        return 1.0 - inv * inv * inv;
    }

    private void SnapshotCurrentGeomInto(Dictionary<int, CardGeom> output)
    {
        output.Clear();
        if (_animTimerToken is null)
        {
            // Idle — just compute the static layout for _index.
            BuildTargetGeomInto(output);
            return;
        }
        var p = Progress();
        // Lerp every index present in either set, without LINQ Union. Anything in _animFrom
        // gets a from-value; we look up a matching to-value (falling back to from for keys
        // only in _animFrom). Then handle keys present only in _animTo.
        foreach (var kv in _animFrom)
        {
            var f = kv.Value;
            var t = _animTo.TryGetValue(kv.Key, out var tv) ? tv : f;
            output[kv.Key] = Lerp(f, t, p);
        }
        foreach (var kv in _animTo)
        {
            if (output.ContainsKey(kv.Key)) continue;
            output[kv.Key] = Lerp(kv.Value, kv.Value, p);
        }
    }

    private void BuildTargetGeomInto(Dictionary<int, CardGeom> output)
    {
        // Walks slots 0, +1, -1, +2, -2 ... from the visual center, computing the geometry of
        // each. Off-screen placement is handled in RetargetTo.
        var viewportWidth = Viewport.Width;
        if (viewportWidth <= 0) return;

        var cx = viewportWidth / 2;

        // Selected card centered on cx.
        var sel = new CardGeom
        {
            LeftX = cx - SelectedWidth / 2.0,
            TopY = 0,
            Width = SelectedWidth,
            Height = SelectedHeight,
            Alpha = SlotAlpha[0],
        };
        output[_index] = sel;

        // Right-side neighbours.
        var rightEdge = sel.LeftX + SelectedWidth + Gap;
        for (var slot = 1; slot <= MaxSlot && slot < _entries.Count; slot++)
        {
            if (rightEdge >= viewportWidth) break;
            var idx = (_index + slot) % _entries.Count;
            if (output.ContainsKey(idx)) break; // wrap collision (very few entries)
            output[idx] = new CardGeom
            {
                LeftX = rightEdge,
                TopY = RowHeight - UnselectedHeight,
                Width = UnselectedWidth,
                Height = UnselectedHeight,
                Alpha = SlotAlpha[Math.Min(slot, SlotAlpha.Length - 1)],
            };
            rightEdge += UnselectedWidth + Gap;
        }

        // Left-side neighbours.
        var leftEdge = sel.LeftX - Gap - UnselectedWidth;
        for (var slot = 1; slot <= MaxSlot && slot < _entries.Count; slot++)
        {
            if (leftEdge + UnselectedWidth <= 0) break;
            var idx = (_index - slot + _entries.Count) % _entries.Count;
            if (output.ContainsKey(idx)) break;
            output[idx] = new CardGeom
            {
                LeftX = leftEdge,
                TopY = RowHeight - UnselectedHeight,
                Width = UnselectedWidth,
                Height = UnselectedHeight,
                Alpha = SlotAlpha[Math.Min(slot, SlotAlpha.Length - 1)],
            };
            leftEdge -= UnselectedWidth + Gap;
        }
    }

    // Off-screen position one slot past where this card currently sits, in the given direction
    // (+1 means slide off to the right, -1 to the left). Used when an index leaves or enters
    // the visible window during a tween.
    private CardGeom OffscreenGeomMatching(CardGeom reference, int direction)
    {
        var step = (UnselectedWidth + Gap) * direction;
        return new CardGeom
        {
            LeftX = reference.LeftX + step,
            TopY = reference.TopY,
            Width = UnselectedWidth,
            Height = UnselectedHeight,
            Alpha = 0.0, // fully dimmed = invisible
        };
    }

    private static CardGeom Lerp(CardGeom a, CardGeom b, double t) => new()
    {
        LeftX = a.LeftX + (b.LeftX - a.LeftX) * t,
        TopY = a.TopY + (b.TopY - a.TopY) * t,
        Width = a.Width + (b.Width - a.Width) * t,
        Height = a.Height + (b.Height - a.Height) * t,
        Alpha = a.Alpha + (b.Alpha - a.Alpha) * t,
    };

    protected override bool OnMouseEvent(Mouse mouse)
    {
        if (mouse.Flags.HasFlag(MouseFlags.WheeledUp))   { MoveLeft();  return true; }
        if (mouse.Flags.HasFlag(MouseFlags.WheeledDown)) { MoveRight(); return true; }

        if (mouse.Flags.HasFlag(MouseFlags.LeftButtonClicked) && mouse.Position is { } pos)
        {
            var hit = HitTest(pos.X, pos.Y);
            if (hit is not null)
            {
                SetFocus();
                if (hit.Value == _index)
                {
                    EntryActivated?.Invoke(this, SelectedTarget);
                }
                else
                {
                    JumpTo(hit.Value);
                }
                return true;
            }
        }

        return base.OnMouseEvent(mouse);
    }

    // Returns the logical entry index under (x, y), or null if the click missed every card.
    // When cards overlap mid-tween (the selected card sits on top of neighbours), the closest-
    // to-center card wins — same z-order the paint path uses.
    private int? HitTest(int x, int y)
    {
        var viewportWidth = Viewport.Width;
        if (viewportWidth <= 0) return null;

        SnapshotCurrentGeomInto(_geomBuf);
        var cx = viewportWidth / 2.0;

        // Copy visible (alpha > 0) entries into the sort buffer, then sort ascending by
        // distance-from-center so the closest card wins ties. Manual insertion sort avoids
        // both the LINQ pipeline and the closure allocation of List.Sort(Comparison).
        _sortBuf.Clear();
        foreach (var kv in _geomBuf)
        {
            if (kv.Value.Alpha > 0.0) _sortBuf.Add(kv);
        }
        for (var i = 1; i < _sortBuf.Count; i++)
        {
            var cur = _sortBuf[i];
            var curD = Math.Abs((cur.Value.LeftX + cur.Value.Width / 2.0) - cx);
            var j = i - 1;
            while (j >= 0)
            {
                var prev = _sortBuf[j];
                var prevD = Math.Abs((prev.Value.LeftX + prev.Value.Width / 2.0) - cx);
                if (prevD <= curD) break;
                _sortBuf[j + 1] = prev;
                j--;
            }
            _sortBuf[j + 1] = cur;
        }

        for (var i = 0; i < _sortBuf.Count; i++)
        {
            var geom = _sortBuf[i].Value;
            var left = (int)Math.Round(geom.LeftX);
            var top = (int)Math.Round(geom.TopY);
            var width = (int)Math.Round(geom.Width);
            var height = (int)Math.Round(geom.Height);
            if (x >= left && x < left + width && y >= top && y < top + height)
            {
                return _sortBuf[i].Key;
            }
        }
        return null;
    }

    private void OnKey(object? sender, Key key)
    {
        if (key == Key.CursorLeft || key.Matches(Key.H))
        {
            MoveLeft();
            key.Handled = true;
            return;
        }
        if (key == Key.CursorRight || key.Matches(Key.L))
        {
            MoveRight();
            key.Handled = true;
            return;
        }
        if (key == Key.Enter)
        {
            key.Handled = true;
            EntryActivated?.Invoke(this, SelectedTarget);
            return;
        }
    }

    protected override bool OnDrawingContent(DrawContext? context)
    {
        var viewportWidth = Viewport.Width;
        if (viewportWidth <= 0) return base.OnDrawingContent(context);

        // Clear the row with the background color.
        SetAttribute(new Attribute(BbsTheme.Body, BbsTheme.Bg));
        for (var y = 0; y < RowHeight; y++)
        {
            for (var x = 0; x < viewportWidth; x++)
            {
                AddRune(x, y, new Rune(' '));
            }
        }

        // Refill the geometry buffer in place — SnapshotCurrentGeomInto handles both the idle
        // case (static target layout) and the in-flight case (lerp between _animFrom / _animTo).
        SnapshotCurrentGeomInto(_geomBuf);

        // Paint farthest-from-center first so the selected card overdraws any seams at the
        // borders. Manual insertion sort over _sortBuf — same shape as HitTest, descending order.
        var cx = viewportWidth / 2.0;
        _sortBuf.Clear();
        foreach (var kv in _geomBuf) _sortBuf.Add(kv);
        for (var i = 1; i < _sortBuf.Count; i++)
        {
            var cur = _sortBuf[i];
            var curD = Math.Abs((cur.Value.LeftX + cur.Value.Width / 2.0) - cx);
            var j = i - 1;
            while (j >= 0)
            {
                var prev = _sortBuf[j];
                var prevD = Math.Abs((prev.Value.LeftX + prev.Value.Width / 2.0) - cx);
                if (prevD >= curD) break;
                _sortBuf[j + 1] = prev;
                j--;
            }
            _sortBuf[j + 1] = cur;
        }

        for (var i = 0; i < _sortBuf.Count; i++)
        {
            var kv = _sortBuf[i];
            PaintCardAt(kv.Value, _entries[kv.Key], viewportWidth);
        }

        return true;
    }

    private void PaintCardAt(CardGeom geom, Entry entry, int viewportWidth)
    {
        if (geom.Alpha <= 0.0) return; // off-screen ghost — nothing to draw

        var leftX = (int)Math.Round(geom.LeftX);
        var topY = (int)Math.Round(geom.TopY);
        var width = (int)Math.Round(geom.Width);
        var height = (int)Math.Round(geom.Height);
        if (width < 4 || height < 3) return;

        var isSelectedStyle = width >= SelectedBorderWidth;
        var alpha = geom.Alpha;

        var borderColor = Dim(isSelectedStyle ? BbsTheme.Accent : BbsTheme.Faint, alpha);
        var labelColor = Dim(isSelectedStyle ? BbsTheme.Header : BbsTheme.Body, alpha);
        var bg = BbsTheme.Bg;

        var (tl, tr, bl, br, h, v) = isSelectedStyle
            ? ('╔', '╗', '╚', '╝', '═', '║')
            : ('┌', '┐', '└', '┘', '─', '│');

        SetAttribute(new Attribute(borderColor, bg, isSelectedStyle ? TextStyle.Bold : TextStyle.None));

        Plot(leftX, topY, new Rune(tl), viewportWidth);
        Plot(leftX + width - 1, topY, new Rune(tr), viewportWidth);
        Plot(leftX, topY + height - 1, new Rune(bl), viewportWidth);
        Plot(leftX + width - 1, topY + height - 1, new Rune(br), viewportWidth);
        for (var x = 1; x < width - 1; x++)
        {
            Plot(leftX + x, topY, new Rune(h), viewportWidth);
            Plot(leftX + x, topY + height - 1, new Rune(h), viewportWidth);
        }
        for (var y = 1; y < height - 1; y++)
        {
            Plot(leftX, topY + y, new Rune(v), viewportWidth);
            Plot(leftX + width - 1, topY + y, new Rune(v), viewportWidth);
        }

        var innerWidth = width - 2;
        var innerLeft = leftX + 1;

        var icon = entry.Icon;
        // Interior rows available for the icon = card height minus top border, bottom border,
        // and label row. The selected card (h=7) has 4; the unselected card (h=5) has 2. Center
        // the icon within whatever's available so 2-row icons keep their old padding above in
        // the selected card, while taller icons (e.g. the 3-row weather glyph) render fully
        // instead of getting clipped to 2 rows.
        var iconAreaRows = Math.Max(0, height - 3);
        var iconRows = Math.Min(iconAreaRows, icon.Height);
        var iconStartY = topY + 1 + (iconAreaRows - iconRows) / 2;
        var iconCols = Math.Min(icon.Width, innerWidth);
        var iconStartX = innerLeft + (innerWidth - iconCols) / 2;
        for (var iy = 0; iy < iconRows; iy++)
        {
            for (var ix = 0; ix < iconCols; ix++)
            {
                var cell = icon[ix, iy];
                var fg = Dim(cell.Foreground, alpha);
                var cbg = Dim(cell.Background, alpha);
                var style = cell.Style.HasFlag(ArtStyle.Bold) ? TextStyle.Bold : TextStyle.None;
                SetAttribute(new Attribute(fg, cbg, style));
                Plot(iconStartX + ix, iconStartY + iy, cell.Glyph, viewportWidth);
            }
        }

        var labelText = isSelectedStyle ? $"► {entry.Label.ToUpperInvariant()} ◄" : entry.Label;
        if (labelText.Length > innerWidth) labelText = labelText[..innerWidth];
        var labelLen = labelText.Length;
        var labelStartX = innerLeft + (innerWidth - labelLen) / 2;
        var labelY = topY + height - 2;

        // Find the first case-insensitive occurrence of the hotkey letter so we can underline
        // it as a subtle binding hint. Hotkeys that aren't a letter in the label (e.g. Key.W
        // for "Browser", Key.F for "Weather", digit keys in the Doors hub) yield -1 here and
        // the loop renders the label without any underline.
        var hotkeyIndex = FindHotkeyIndex(labelText, entry.Hotkey);
        var baseStyle = isSelectedStyle ? TextStyle.Bold : TextStyle.None;
        var baseAttr = new Attribute(labelColor, bg, baseStyle);
        var hotkeyAttr = new Attribute(labelColor, bg, baseStyle | TextStyle.Underline);

        SetAttribute(baseAttr);
        for (var i = 0; i < labelLen; i++)
        {
            if (i == hotkeyIndex)
            {
                SetAttribute(hotkeyAttr);
                Plot(labelStartX + i, labelY, new Rune(labelText[i]), viewportWidth);
                SetAttribute(baseAttr);
            }
            else
            {
                Plot(labelStartX + i, labelY, new Rune(labelText[i]), viewportWidth);
            }
        }
    }

    private static int FindHotkeyIndex(string labelText, Key hotkey)
    {
        var rune = hotkey.AsRune;
        if (rune.Value == 0) return -1;
        var target = char.ToLowerInvariant((char)rune.Value);
        for (var i = 0; i < labelText.Length; i++)
        {
            if (char.ToLowerInvariant(labelText[i]) == target) return i;
        }
        return -1;
    }

    // Blend a color toward BbsTheme.Bg by (1 - alpha). alpha=1.0 returns the input, alpha=0.0
    // returns the background. Used to dim cards by distance from center and to fade ghosts off
    // the screen edge.
    internal static Color Dim(Color input, double alpha)
    {
        if (alpha >= 1.0) return input;
        if (alpha <= 0.0) return BbsTheme.Bg;
        var bg = BbsTheme.Bg;
        var r = (byte)Math.Round(bg.R + (input.R - bg.R) * alpha);
        var g = (byte)Math.Round(bg.G + (input.G - bg.G) * alpha);
        var b = (byte)Math.Round(bg.B + (input.B - bg.B) * alpha);
        return new Color(r, g, b, 0xFF);
    }

    internal static Color Dim(ArtColor input, double alpha)
    {
        if (alpha >= 1.0) return new Color(input.R, input.G, input.B, 0xFF);
        if (alpha <= 0.0) return BbsTheme.Bg;
        var bg = BbsTheme.Bg;
        var r = (byte)Math.Round(bg.R + (input.R - bg.R) * alpha);
        var g = (byte)Math.Round(bg.G + (input.G - bg.G) * alpha);
        var b = (byte)Math.Round(bg.B + (input.B - bg.B) * alpha);
        return new Color(r, g, b, 0xFF);
    }

    private void Plot(int x, int y, Rune r, int viewportWidth)
    {
        if (x < 0 || x >= viewportWidth) return;
        if (y < 0 || y >= RowHeight) return;
        AddRune(x, y, r);
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing && _animTimerToken is not null)
        {
            try { _app.RemoveTimeout(_animTimerToken); } catch { /* ignore */ }
            _animTimerToken = null;
        }
        base.Dispose(disposing);
    }
}
