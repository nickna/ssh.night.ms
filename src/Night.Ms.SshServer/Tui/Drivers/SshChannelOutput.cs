using System.Drawing;
using System.Text;
using Terminal.Gui.Drivers;

namespace Night.Ms.SshServer.Tui.Drivers;

// Renders Terminal.Gui's IOutputBuffer to an SSH channel as ANSI escape sequences.
// OutputBase already handles the dirty-cell walk + sequence batching; we only have to (a) push
// bytes to the channel and (b) report the PTY size that came in via the SSH session's pty-req
// (and, later, window-change) events.
//
// Write strategy: a single session-scoped UTF-8 byte buffer accumulates every fragment produced
// during a frame, and Flush() drains the whole frame in one _stream.Write + _stream.Flush call.
// The earlier shape rented from ArrayPool and called _stream.Write *per SGR fragment* —
// OutputBase emits dozens of fragments per frame, so the old shape was N small stream writes
// per frame per session. This shape is one large write per frame per session.
internal sealed class SshChannelOutput : OutputBase, IOutput
{
    private readonly Stream _stream;
    private readonly Func<Size> _getSize;
    private Cursor _currentCursor = new();
    private bool _disposed;

    // Reusable per-session UTF-8 write buffer. Grows monotonically (Array.Resize) to the
    // largest frame seen — typically settles at a few KB and stays there. 16 KB initial is
    // enough for a full 80×24 repaint with SGR changes.
    private byte[] _outBuf = new byte[16 * 1024];
    private int _outLen;

    public SshChannelOutput(Stream stream, Func<Size> getSize)
    {
        _stream = stream;
        _getSize = getSize;

        // SSH clients with a PTY always handle modern ANSI sequences; we don't need legacy console.
        IsLegacyConsole = false;

        // Activate alternate screen buffer, clear, hide cursor, enable mouse (SGR 1006).
        WriteRaw(EscSeqUtils.CSI_SaveCursorAndActivateAltBufferNoBackscroll);
        WriteRaw(EscSeqUtils.CSI_ClearScreen(EscSeqUtils.ClearScreenOptions.EntireScreen));
        WriteRaw(EscSeqUtils.CSI_SetCursorPosition(1, 1));
        WriteRaw(EscSeqUtils.CSI_HideCursor);
        WriteRaw(EscSeqUtils.CSI_EnableMouseEvents);
        Flush();
    }

    public Size GetSize() => _getSize();

    public void SetSize(int width, int height)
    {
        // Driven by the SSH window-change request via _getSize; this hook is unused.
    }

    public Cursor GetCursor() => _currentCursor;

    public void SetCursor(Cursor cursor)
    {
        try
        {
            if (!cursor.IsVisible)
            {
                WriteRaw(EscSeqUtils.CSI_HideCursor);
            }
            else
            {
                if (_currentCursor.Style != cursor.Style)
                {
                    WriteRaw(EscSeqUtils.CSI_SetCursorStyle(cursor.Style));
                }
                WriteRaw(EscSeqUtils.CSI_ShowCursor);
            }
        }
        finally
        {
            SetCursorPositionImpl(cursor.Position?.X ?? 0, cursor.Position?.Y ?? 0);
            _currentCursor = cursor;
            // Flush so the cursor sequence reaches the wire this frame. Terminal.Gui calls
            // SetCursor after Write(IOutputBuffer) returns; without an explicit Flush here
            // the bytes would sit in _outBuf until the next frame, lagging the user-visible
            // cursor by one frame and making input feel sluggish.
            Flush();
        }
    }

    public void Suspend()
    {
        // SSH sessions don't suspend the way local terminals do.
    }

    public void Write(ReadOnlySpan<char> text) => WriteRawCore(text);

    public override void Write(IOutputBuffer buffer)
    {
        base.Write(buffer);
        Flush();
    }

    protected override bool SetCursorPositionImpl(int screenPositionX, int screenPositionY)
    {
        if (_currentCursor.Position is { } pos && pos.X == screenPositionX && pos.Y == screenPositionY)
        {
            return false;
        }
        // ANSI rows/cols are 1-based.
        WriteRaw(EscSeqUtils.CSI_SetCursorPosition(screenPositionY + 1, screenPositionX + 1));
        return true;
    }

    // StringBuilder is iterated chunk-by-chunk so we don't allocate a backing string just to
    // hand it to the UTF-8 encoder. The base class produces small SGR + cell-content fragments
    // that almost always fit in a single chunk; chunked iteration costs nothing extra.
    protected override void Write(StringBuilder output)
    {
        if (_disposed || output.Length == 0) return;
        foreach (var chunk in output.GetChunks())
        {
            WriteRawCore(chunk.Span);
        }
    }

    private void WriteRaw(string text)
    {
        if (string.IsNullOrEmpty(text)) return;
        WriteRawCore(text.AsSpan());
    }

    // Encodes text directly into the session-scoped _outBuf at _outLen, growing the buffer if
    // needed. No allocation, no syscall — bytes sit in the buffer until Flush().
    private void WriteRawCore(ReadOnlySpan<char> text)
    {
        if (_disposed || text.IsEmpty) return;

        var byteCount = Encoding.UTF8.GetByteCount(text);
        var needed = _outLen + byteCount;
        if (needed > _outBuf.Length)
        {
            // Grow by powers of two so repeated grows stay O(1) amortized. Caps at whatever
            // the largest frame demands; subsequent frames reuse the buffer in place.
            var newSize = _outBuf.Length;
            while (newSize < needed) newSize *= 2;
            Array.Resize(ref _outBuf, newSize);
        }

        var written = Encoding.UTF8.GetBytes(text, _outBuf.AsSpan(_outLen));
        _outLen += written;
    }

    // Drains the accumulated buffer in one _stream.Write + _stream.Flush. Called at the end
    // of Write(IOutputBuffer) (per frame) and from ctor/Dispose for the alt-screen toggles.
    private void Flush()
    {
        if (_outLen > 0)
        {
            try { _stream.Write(_outBuf, 0, _outLen); }
            catch { /* channel closed — swallow so the main loop can shut down cleanly */ }
            _outLen = 0;
        }
        try { _stream.Flush(); }
        catch { /* channel closed */ }
    }

    public void Dispose()
    {
        if (_disposed) return;
        try
        {
            // Order matters: _disposed must be set *after* these writes, because WriteRawCore
            // early-returns on _disposed. The original code set it first and silently dropped
            // every cleanup sequence, leaving the terminal in alternate-screen + mouse-tracking
            // + hidden-cursor state on clean disconnect.
            WriteRaw(EscSeqUtils.CSI_DisableMouseEvents);
            WriteRaw(EscSeqUtils.CSI_ResetAttributes);
            WriteRaw(EscSeqUtils.CSI_RestoreCursorAndRestoreAltBufferWithBackscroll);
            WriteRaw(EscSeqUtils.CSI_ShowCursor);
            Flush();
        }
        catch
        {
            // best-effort
        }
        finally
        {
            _disposed = true;
        }
    }
}
