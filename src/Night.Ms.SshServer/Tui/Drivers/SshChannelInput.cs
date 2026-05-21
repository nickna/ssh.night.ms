using System.Collections.Concurrent;
using System.Text;
using Terminal.Gui.Drivers;

namespace Night.Ms.SshServer.Tui.Drivers;

// Reads UTF-8 bytes from an SSH channel input stream, decodes to chars, and surfaces them via
// the InputImpl<char> Peek/Read contract. A background pump task hides the blocking ReadAsync
// behind a lock-free queue so Peek can be cheap.
internal sealed class SshChannelInput : InputImpl<char>
{
    // Cap on buffered (decoded) chars. A megabyte of paste at 5 cps still consumes the
    // backlog inside a couple seconds at normal typing latency, while keeping a paste-bomb
    // from filling memory faster than the UI thread can drain. Overflow drops *new* input
    // — a partially-applied paste is less confusing than losing the start of a typed line.
    private const int MaxPendingChars = 64 * 1024;

    private readonly Stream _stream;
    private readonly ConcurrentQueue<char> _pending = new();
    private int _pendingCount;
    private readonly CancellationTokenSource _pumpCts = new();
    private readonly Task _pumpTask;

    public SshChannelInput(Stream stream)
    {
        _stream = stream;
        // Start the pump in the ctor — the old lazy-start variant had a non-atomic
        // "is _pumpTask null?" check that could race two callers into starting two pumps
        // reading the same stream. Single deterministic start here avoids the race entirely.
        _pumpTask = Task.Run(PumpAsync);
    }

    public override bool Peek() => !_pending.IsEmpty;

    public override IEnumerable<char> Read()
    {
        while (_pending.TryDequeue(out var c))
        {
            Interlocked.Decrement(ref _pendingCount);
            yield return c;
        }
    }

    private async Task PumpAsync()
    {
        var buffer = new byte[1024];
        var decoder = Encoding.UTF8.GetDecoder();
        var charBuffer = new char[1024];

        try
        {
            while (!_pumpCts.IsCancellationRequested)
            {
                int read;
                try
                {
                    read = await _stream.ReadAsync(buffer.AsMemory(), _pumpCts.Token).ConfigureAwait(false);
                }
                catch (Exception) when (_pumpCts.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception)
                {
                    // Channel closed by peer; exit loop.
                    return;
                }

                if (read == 0) return;

                var charsWritten = decoder.GetChars(buffer, 0, read, charBuffer, 0, flush: false);
                for (var i = 0; i < charsWritten; i++)
                {
                    // Drop-newest on overflow. We check before the enqueue so a megabyte
                    // paste doesn't get fully queued and then dropped piecemeal — it just
                    // stops accumulating past the cap. The reader's Interlocked.Decrement
                    // frees space as the UI thread drains.
                    if (Volatile.Read(ref _pendingCount) >= MaxPendingChars) break;
                    _pending.Enqueue(charBuffer[i]);
                    Interlocked.Increment(ref _pendingCount);
                }
            }
        }
        catch (OperationCanceledException)
        {
            // expected on dispose
        }
    }


    public override void Dispose()
    {
        try
        {
            _pumpCts.Cancel();
        }
        catch
        {
            // best-effort
        }

        // Don't block the caller waiting for the pump to unwind from its in-flight
        // ReadAsync — on session teardown that costs up to 200ms of UI-thread stall.
        // Hand the CTS off to a continuation that fires once the pump actually exits,
        // so the token stays alive for the pump's lifetime and Dispose() returns now.
        _ = _pumpTask.ContinueWith(
            static (_, state) => ((CancellationTokenSource)state!).Dispose(),
            _pumpCts,
            CancellationToken.None,
            TaskContinuationOptions.ExecuteSynchronously,
            TaskScheduler.Default);

        base.Dispose();
    }
}
