using System.Text;
using System.Text.RegularExpressions;

namespace Night.Ms.Tools.LoadTest.Bots;

// Rolling UTF-8 → char buffer fed by a background read loop off the bot's ShellStream.
// Decoder handles split multi-byte UTF-8 across chunks. Buffer is capped (oldest chars
// drop off the head) so a long-running bot can't balloon memory. Grep helpers strip
// ANSI/SGR sequences before searching so the regex doesn't have to navigate CSI noise.
public sealed class ScreenBuffer : IAsyncDisposable
{
    private const int Capacity = 32 * 1024;
    private const int ReadBufferSize = 4096;

    // Matches the bulk of what Terminal.Gui emits: CSI sequences (cursor moves, SGR
    // colors), simple escape sequences like ESC[H, OSC titles ending in ST/BEL, and bare
    // C1 controls. Good enough for screen-scraping at scale — doesn't try to be a
    // complete VT520 parser.
    private static readonly Regex AnsiRegex = new(
        @"\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07\x1B]*(?:\x07|\x1B\\))",
        RegexOptions.Compiled);

    private readonly Stream _stream;
    private readonly StringBuilder _raw = new(Capacity);
    private readonly Decoder _decoder = Encoding.UTF8.GetDecoder();
    private readonly object _gate = new();
    private readonly CancellationTokenSource _readCts = new();
    private Task? _readTask;

    public ScreenBuffer(Stream stream)
    {
        _stream = stream;
    }

    public void Start()
    {
        if (_readTask is not null) return;
        _readTask = Task.Run(PumpAsync);
    }

    private async Task PumpAsync()
    {
        var bytes = new byte[ReadBufferSize];
        var chars = new char[ReadBufferSize];
        try
        {
            while (!_readCts.IsCancellationRequested)
            {
                int read;
                try
                {
                    read = await _stream.ReadAsync(bytes.AsMemory(), _readCts.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException) { return; }
                catch { return; }
                if (read == 0) return;

                var charsWritten = _decoder.GetChars(bytes, 0, read, chars, 0, flush: false);
                lock (_gate)
                {
                    _raw.Append(chars, 0, charsWritten);
                    if (_raw.Length > Capacity)
                    {
                        // Drop oldest. StringBuilder.Remove on the head is O(n); at our
                        // 32 KB cap and chat-paced traffic this is well under a ms.
                        _raw.Remove(0, _raw.Length - Capacity);
                    }
                }
            }
        }
        catch (OperationCanceledException) { /* expected */ }
    }

    public string Snapshot()
    {
        lock (_gate) return _raw.ToString();
    }

    public string StrippedSnapshot()
    {
        var raw = Snapshot();
        return AnsiRegex.Replace(raw, string.Empty);
    }

    public bool Contains(string substring)
    {
        var stripped = StrippedSnapshot();
        return stripped.Contains(substring, StringComparison.Ordinal);
    }

    public async Task<bool> WaitForAsync(string substring, TimeSpan timeout, CancellationToken ct)
    {
        var deadline = DateTimeOffset.UtcNow + timeout;
        while (DateTimeOffset.UtcNow < deadline)
        {
            ct.ThrowIfCancellationRequested();
            if (Contains(substring)) return true;
            await Task.Delay(50, ct).ConfigureAwait(false);
        }
        return false;
    }

    public async Task<Match?> WaitForAsync(Regex pattern, TimeSpan timeout, CancellationToken ct)
    {
        var deadline = DateTimeOffset.UtcNow + timeout;
        while (DateTimeOffset.UtcNow < deadline)
        {
            ct.ThrowIfCancellationRequested();
            var m = pattern.Match(StrippedSnapshot());
            if (m.Success) return m;
            await Task.Delay(50, ct).ConfigureAwait(false);
        }
        return null;
    }

    public async ValueTask DisposeAsync()
    {
        try
        {
            _readCts.Cancel();
            if (_readTask is not null)
            {
                await Task.WhenAny(_readTask, Task.Delay(500)).ConfigureAwait(false);
            }
        }
        catch { /* best effort */ }
        finally
        {
            _readCts.Dispose();
        }
    }
}
