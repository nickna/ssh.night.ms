using System.Buffers;
using System.IO.Pipelines;
using System.Net.WebSockets;
using System.Text;
using Night.Ms.SshServer.Web;

namespace Night.Ms.SshServer.Tests;

public class WebSocketDuplexStreamTests
{
    [Fact]
    public async Task Binary_frame_received_by_server_surfaces_through_ReadAsync()
    {
        await using var pair = WebSocketPair.Create();
        await using var sut = new WebSocketDuplexStream(pair.Server);

        var payload = new byte[] { 0x68, 0x69, 0x21 }; // "hi!"
        await pair.Client.SendAsync(payload, WebSocketMessageType.Binary, true, default);

        var buffer = new byte[16];
        var read = await ReadAtLeastAsync(sut, buffer, payload.Length);
        Assert.Equal(payload, buffer[..read]);
    }

    [Fact]
    public async Task WriteAsync_sends_a_single_binary_frame()
    {
        await using var pair = WebSocketPair.Create();
        await using var sut = new WebSocketDuplexStream(pair.Server);

        var payload = Encoding.UTF8.GetBytes("\x1b[2J");
        await sut.WriteAsync(payload);

        var buffer = new byte[64];
        var result = await pair.Client.ReceiveAsync(buffer, default);
        Assert.Equal(WebSocketMessageType.Binary, result.MessageType);
        Assert.True(result.EndOfMessage);
        Assert.Equal(payload, buffer[..result.Count]);
    }

    [Fact]
    public async Task Text_frame_resize_updates_pty_and_fires_window_changed()
    {
        await using var pair = WebSocketPair.Create();
        await using var sut = new WebSocketDuplexStream(pair.Server);
        Night.Ms.SshTransport.WindowChange? observed = null;
        sut.WindowChanged += (_, c) => observed = c;

        var json = """{"type":"resize","cols":120,"rows":40}"""u8.ToArray();
        await pair.Client.SendAsync(json, WebSocketMessageType.Text, true, default);

        // The pump runs on a background task; spin briefly until it processes the frame.
        var deadline = DateTime.UtcNow.AddSeconds(2);
        while (observed is null && DateTime.UtcNow < deadline)
        {
            await Task.Delay(20);
        }

        Assert.NotNull(observed);
        Assert.Equal(120u, observed!.Cols);
        Assert.Equal(40u, observed.Rows);
        Assert.NotNull(sut.Pty);
        Assert.Equal(120u, sut.Pty!.Cols);
        Assert.Equal(40u, sut.Pty.Rows);
    }

    [Fact]
    public async Task Unknown_text_frame_type_is_ignored_silently()
    {
        await using var pair = WebSocketPair.Create();
        await using var sut = new WebSocketDuplexStream(pair.Server);
        var fired = 0;
        sut.WindowChanged += (_, _) => Interlocked.Increment(ref fired);

        var json = """{"type":"jiggle","cols":42}"""u8.ToArray();
        await pair.Client.SendAsync(json, WebSocketMessageType.Text, true, default);
        await Task.Delay(80);

        Assert.Equal(0, fired);
        Assert.Null(sut.Pty);
    }

    [Fact]
    public async Task Close_frame_causes_ReadAsync_to_return_zero()
    {
        await using var pair = WebSocketPair.Create();
        await using var sut = new WebSocketDuplexStream(pair.Server);

        await pair.Client.CloseAsync(WebSocketCloseStatus.NormalClosure, "bye", default);

        var buffer = new byte[16];
        var read = await sut.ReadAsync(buffer, new CancellationTokenSource(TimeSpan.FromSeconds(2)).Token);
        Assert.Equal(0, read);
    }

    [Fact]
    public async Task Ping_text_frame_is_a_noop()
    {
        await using var pair = WebSocketPair.Create();
        await using var sut = new WebSocketDuplexStream(pair.Server);

        var json = """{"type":"ping"}"""u8.ToArray();
        await pair.Client.SendAsync(json, WebSocketMessageType.Text, true, default);
        await Task.Delay(50);
        Assert.Null(sut.Pty);
    }

    // Reads until we have at least minBytes (or stream closes). WebSocketDuplexStream surfaces
    // each frame as its own ReadAsync result; small payloads fit in one call but explicit looping
    // keeps the test robust to that.
    private static async Task<int> ReadAtLeastAsync(Stream s, byte[] buffer, int minBytes)
    {
        var total = 0;
        var deadline = DateTime.UtcNow.AddSeconds(2);
        while (total < minBytes && DateTime.UtcNow < deadline)
        {
            using var cts = new CancellationTokenSource(TimeSpan.FromMilliseconds(500));
            int n;
            try { n = await s.ReadAsync(buffer.AsMemory(total), cts.Token); }
            catch (OperationCanceledException) { break; }
            if (n == 0) break;
            total += n;
        }
        return total;
    }
}

// Pairs two System.Net.WebSockets.WebSocket instances over an in-memory duplex stream so we
// can drive a WebSocket end-to-end inside an xUnit test without TestHost.
internal sealed class WebSocketPair : IAsyncDisposable
{
    public WebSocket Server { get; }
    public WebSocket Client { get; }
    private readonly DuplexStreamPair _streams;

    private WebSocketPair(WebSocket server, WebSocket client, DuplexStreamPair streams)
    {
        Server = server;
        Client = client;
        _streams = streams;
    }

    public static WebSocketPair Create()
    {
        var streams = new DuplexStreamPair();
        var server = WebSocket.CreateFromStream(streams.SideA, isServer: true, subProtocol: null, keepAliveInterval: TimeSpan.FromMinutes(1));
        var client = WebSocket.CreateFromStream(streams.SideB, isServer: false, subProtocol: null, keepAliveInterval: TimeSpan.FromMinutes(1));
        return new WebSocketPair(server, client, streams);
    }

    public async ValueTask DisposeAsync()
    {
        try { Client.Dispose(); } catch { }
        try { Server.Dispose(); } catch { }
        await _streams.DisposeAsync();
    }
}

// Two cross-wired duplex streams over Pipe<T> — what one side writes, the other side reads.
internal sealed class DuplexStreamPair : IAsyncDisposable
{
    public Stream SideA { get; }
    public Stream SideB { get; }
    private readonly Pipe _aToB = new();
    private readonly Pipe _bToA = new();

    public DuplexStreamPair()
    {
        SideA = new DuplexPipeStream(_bToA.Reader, _aToB.Writer);
        SideB = new DuplexPipeStream(_aToB.Reader, _bToA.Writer);
    }

    public async ValueTask DisposeAsync()
    {
        await _aToB.Writer.CompleteAsync();
        await _bToA.Writer.CompleteAsync();
        await _aToB.Reader.CompleteAsync();
        await _bToA.Reader.CompleteAsync();
    }
}

internal sealed class DuplexPipeStream(PipeReader reader, PipeWriter writer) : Stream
{
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
    public override void Flush() { }
    public override Task FlushAsync(CancellationToken cancellationToken) => writer.FlushAsync(cancellationToken).AsTask();

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
    {
        var res = await reader.ReadAsync(cancellationToken).ConfigureAwait(false);
        if (res.Buffer.IsEmpty && res.IsCompleted) return 0;
        var slice = res.Buffer.Slice(0, Math.Min(res.Buffer.Length, buffer.Length));
        slice.CopyTo(buffer.Span);
        var n = (int)slice.Length;
        reader.AdvanceTo(slice.End);
        return n;
    }

    public override int Read(byte[] buffer, int offset, int count)
        => ReadAsync(buffer.AsMemory(offset, count)).AsTask().GetAwaiter().GetResult();

    public override async ValueTask WriteAsync(ReadOnlyMemory<byte> buffer, CancellationToken cancellationToken = default)
    {
        await writer.WriteAsync(buffer, cancellationToken).ConfigureAwait(false);
    }

    public override void Write(byte[] buffer, int offset, int count)
        => WriteAsync(buffer.AsMemory(offset, count)).AsTask().GetAwaiter().GetResult();
}
