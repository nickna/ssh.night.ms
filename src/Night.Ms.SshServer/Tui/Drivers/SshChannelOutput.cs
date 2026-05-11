using System.Drawing;
using System.Text;
using Terminal.Gui.Drivers;

namespace Night.Ms.SshServer.Tui.Drivers;

// Renders Terminal.Gui's IOutputBuffer to an SSH channel as ANSI escape sequences.
// OutputBase already handles the dirty-cell walk + sequence batching; we only have to (a) push
// bytes to the channel and (b) report the PTY size that came in via the SSH session's pty-req
// (and, later, window-change) events.
internal sealed class SshChannelOutput : OutputBase, IOutput
{
    private readonly Stream _stream;
    private readonly Func<Size> _getSize;
    private readonly StringBuilder _lastOutputCapture = new();
    private bool _clearLastOutputPending;
    private Cursor _currentCursor = new();
    private Size _lastReportedSize;
    private bool _disposed;

    public SshChannelOutput(Stream stream, Func<Size> getSize)
    {
        _stream = stream;
        _getSize = getSize;
        _lastReportedSize = getSize();

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

    public void SetSize(int width, int height) => _lastReportedSize = new Size(width, height);

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
        }
    }

    public void Suspend()
    {
        // SSH sessions don't suspend the way local terminals do.
    }

    public void Write(ReadOnlySpan<char> text)
    {
        var sb = new StringBuilder();
        sb.Append(text);
        WriteToBuffer(sb);
        WriteRawCore(text);
    }

    public override void Write(IOutputBuffer buffer)
    {
        _clearLastOutputPending = true;
        base.Write(buffer);
        Flush();
    }

    public override string GetLastOutput() => _lastOutputCapture.ToString();

    public new string ToAnsi(IOutputBuffer buffer) => base.ToAnsi(buffer);

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

    protected override void Write(StringBuilder output)
    {
        WriteToBuffer(output);
        WriteRawCore(output.ToString().AsSpan());
    }

    private void WriteRaw(string text)
    {
        if (string.IsNullOrEmpty(text)) return;
        WriteRawCore(text.AsSpan());
    }

    private void WriteRawCore(ReadOnlySpan<char> text)
    {
        if (_disposed || text.IsEmpty) return;
        try
        {
            var bytes = Encoding.UTF8.GetBytes(text.ToArray());
            _stream.Write(bytes, 0, bytes.Length);
        }
        catch (Exception)
        {
            // The channel may have closed under us — swallow so the main loop can shut down cleanly.
        }
    }

    private void WriteToBuffer(StringBuilder output)
    {
        if (_clearLastOutputPending)
        {
            _lastOutputCapture.Clear();
            _clearLastOutputPending = false;
        }
        _lastOutputCapture.Append(output);
    }

    private void Flush()
    {
        try { _stream.Flush(); }
        catch { /* channel closed */ }
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        try
        {
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
    }
}
