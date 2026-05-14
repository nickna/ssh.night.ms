using System.Buffers;
using System.IO.Pipelines;
using System.Net.WebSockets;
using System.Text.Json;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Web;

// Adapts a System.Net.WebSockets.WebSocket to a duplex Stream so the existing TG driver
// (which speaks bytes against a Stream) can run over a browser WebSocket unchanged.
//
// Wire protocol:
//   browser -> server: binary frames carry raw stdin bytes; text frames carry JSON control
//                      messages, currently only { "type": "resize", "cols": N, "rows": N }
//                      and { "type": "ping" }. Unknown text payloads are logged and dropped.
//   server -> browser: every Write() emits a single binary frame containing the bytes verbatim.
//
// A single background pump task drains the WebSocket. Binary payloads flow through an internal
// Pipe so ReadAsync stays a simple PipeReader.ReadAsync. Text payloads update Pty and raise
// WindowChanged so the driver picks up the new size on the next render tick. A Close frame
// completes the writer; subsequent ReadAsync calls return 0.
public sealed class WebSocketDuplexStream : Stream, IAsyncDisposable
{
    private readonly WebSocket _ws;
    private readonly Pipe _inbound = new();
    private readonly CancellationTokenSource _pumpCts = new();
    private readonly Task _pumpTask;
    private readonly ILogger? _logger;
    private readonly SemaphoreSlim _writeLock = new(1, 1);
    private PtyInfo? _pty;
    private bool _disposed;

    public WebSocketDuplexStream(WebSocket ws, PtyInfo? initialPty = null, ILogger? logger = null)
    {
        _ws = ws;
        _pty = initialPty;
        _logger = logger;
        _pumpTask = Task.Run(PumpAsync);
    }

    // Mutable so resize messages can update it in place; the runner's GetSize delegate
    // re-reads on each render tick.
    public PtyInfo? Pty => _pty;

    public event EventHandler<WindowChange>? WindowChanged;

    public override bool CanRead => true;
    public override bool CanWrite => true;
    public override bool CanSeek => false;
    public override long Length => throw new NotSupportedException();
    public override long Position
    {
        get => throw new NotSupportedException();
        set => throw new NotSupportedException();
    }
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
    {
        var result = await _inbound.Reader.ReadAsync(cancellationToken).ConfigureAwait(false);
        if (result.Buffer.IsEmpty && result.IsCompleted) return 0;

        var slice = result.Buffer.Slice(0, Math.Min(result.Buffer.Length, buffer.Length));
        slice.CopyTo(buffer.Span);
        var consumed = (int)slice.Length;
        _inbound.Reader.AdvanceTo(slice.End);
        return consumed;
    }

    public override int Read(byte[] buffer, int offset, int count)
        => ReadAsync(buffer.AsMemory(offset, count), CancellationToken.None).AsTask().GetAwaiter().GetResult();

    public override async ValueTask WriteAsync(ReadOnlyMemory<byte> buffer, CancellationToken cancellationToken = default)
    {
        if (_disposed || _ws.State != WebSocketState.Open) return;
        // SendAsync isn't safe under concurrent callers; the driver writes a frame per render
        // pass and there can be additional Flushes, so serialize.
        await _writeLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            await _ws.SendAsync(buffer, WebSocketMessageType.Binary, endOfMessage: true, cancellationToken).ConfigureAwait(false);
        }
        catch (WebSocketException)
        {
            // peer closed mid-write; the pump will observe Close shortly
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            _writeLock.Release();
        }
    }

    public override void Write(byte[] buffer, int offset, int count)
        => WriteAsync(buffer.AsMemory(offset, count), CancellationToken.None).AsTask().GetAwaiter().GetResult();

    public override void Flush() { /* no buffering — WriteAsync sends immediately */ }
    public override Task FlushAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    private async Task PumpAsync()
    {
        var rented = System.Buffers.ArrayPool<byte>.Shared.Rent(8192);
        try
        {
            while (!_pumpCts.IsCancellationRequested && _ws.State == WebSocketState.Open)
            {
                ValueWebSocketReceiveResult res;
                try
                {
                    res = await _ws.ReceiveAsync(rented.AsMemory(), _pumpCts.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException) { break; }
                catch (WebSocketException) { break; }

                if (res.MessageType == WebSocketMessageType.Close)
                {
                    // Complete the close handshake so the peer's CloseAsync (which waits for
                    // our ack frame) doesn't hang. Best-effort: the WS may already be torn
                    // down by the time we send.
                    try
                    {
                        if (_ws.State == WebSocketState.CloseReceived)
                        {
                            await _ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "ack", CancellationToken.None).ConfigureAwait(false);
                        }
                    }
                    catch { }
                    break;
                }

                if (res.MessageType == WebSocketMessageType.Binary)
                {
                    // A single logical message can span multiple frames if EndOfMessage is
                    // false. Surface each chunk to the pipe as it arrives — the driver doesn't
                    // care about message boundaries on stdin.
                    if (res.Count > 0)
                    {
                        var dest = _inbound.Writer.GetMemory(res.Count);
                        rented.AsMemory(0, res.Count).CopyTo(dest);
                        _inbound.Writer.Advance(res.Count);
                        var flush = await _inbound.Writer.FlushAsync(_pumpCts.Token).ConfigureAwait(false);
                        if (flush.IsCompleted) break;
                    }
                    continue;
                }

                if (res.MessageType == WebSocketMessageType.Text)
                {
                    // Text payloads are control messages only. Accumulate until EndOfMessage
                    // then parse — control payloads are tiny so a single buffer is fine.
                    using var ms = new MemoryStream();
                    ms.Write(rented, 0, res.Count);
                    while (!res.EndOfMessage && !_pumpCts.IsCancellationRequested)
                    {
                        try
                        {
                            res = await _ws.ReceiveAsync(rented.AsMemory(), _pumpCts.Token).ConfigureAwait(false);
                        }
                        catch { res = default; break; }
                        if (res.MessageType != WebSocketMessageType.Text) break;
                        ms.Write(rented, 0, res.Count);
                    }
                    HandleControlMessage(ms.ToArray());
                    continue;
                }
            }
        }
        finally
        {
            System.Buffers.ArrayPool<byte>.Shared.Return(rented);
            await _inbound.Writer.CompleteAsync().ConfigureAwait(false);
        }
    }

    private void HandleControlMessage(byte[] payload)
    {
        try
        {
            using var doc = JsonDocument.Parse(payload);
            if (!doc.RootElement.TryGetProperty("type", out var typeProp)) return;
            var type = typeProp.GetString();
            switch (type)
            {
                case "resize":
                    var cols = doc.RootElement.TryGetProperty("cols", out var c) ? (uint)Math.Max(1, c.GetInt32()) : (uint)80;
                    var rows = doc.RootElement.TryGetProperty("rows", out var r) ? (uint)Math.Max(1, r.GetInt32()) : (uint)24;
                    _pty = new PtyInfo(Terminal: _pty?.Terminal ?? "xterm-256color", cols, rows, PixelWidth: 0, PixelHeight: 0);
                    WindowChanged?.Invoke(this, new WindowChange(cols, rows, 0, 0));
                    break;
                case "ping":
                    // No-op for now; here so a future client can keep the channel warm without
                    // shipping payload bytes that would land in the TUI input stream.
                    break;
                default:
                    _logger?.LogDebug("Ignoring WS text frame with unknown type='{Type}'", type);
                    break;
            }
        }
        catch (JsonException ex)
        {
            _logger?.LogDebug(ex, "Ignoring malformed WS text frame");
        }
    }

    public override async ValueTask DisposeAsync()
    {
        if (_disposed) return;
        _disposed = true;
        try { _pumpCts.Cancel(); } catch { }
        try { await _pumpTask.ConfigureAwait(false); } catch { }
        try
        {
            if (_ws.State == WebSocketState.Open)
            {
                await _ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "bye", CancellationToken.None).ConfigureAwait(false);
            }
        }
        catch { }
        _pumpCts.Dispose();
        _writeLock.Dispose();
        await base.DisposeAsync().ConfigureAwait(false);
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing && !_disposed)
        {
            // Synchronous dispose: kick off the cleanup and let it finish in the background.
            // ASP.NET endpoint lifetimes call DisposeAsync so this path is rarely taken.
            _ = DisposeAsync().AsTask();
        }
        base.Dispose(disposing);
    }
}
