using System.Text;
using Renci.SshNet;

namespace Night.Ms.Tools.LoadTest.Bots;

// One synthetic SSH session: a Renci.SshNet client opening a single shell channel with a
// PTY sized 120x40 (wide enough that chat history doesn't word-wrap badly, tall enough
// the carousel + welcome both fit). The associated ScreenBuffer pumps reads in the
// background so scenarios can `WaitForAsync` against rendered TUI markers without
// blocking writes.
public sealed class Bot : IAsyncDisposable
{
    private readonly string _host;
    private readonly int _port;
    private readonly string _handle;
    private readonly string _privateKeyPath;

    private SshClient? _client;
    private ShellStream? _stream;
    private ScreenBuffer? _screen;

    public Bot(string host, int port, string handle, string privateKeyPath)
    {
        _host = host;
        _port = port;
        _handle = handle;
        _privateKeyPath = privateKeyPath;
    }

    public string Handle => _handle;
    public ScreenBuffer Screen => _screen ?? throw new InvalidOperationException("Bot.ConnectAsync hasn't completed.");

    public async Task ConnectAsync(CancellationToken ct)
    {
        var keyFile = new PrivateKeyFile(_privateKeyPath);
        var auth = new PrivateKeyAuthenticationMethod(_handle, keyFile);
        var info = new ConnectionInfo(_host, _port, _handle, auth)
        {
            // Default is 30s, which is generous; we tighten so a stuck handshake
            // surfaces fast during ramp instead of pinning a task slot.
            Timeout = TimeSpan.FromSeconds(15),
        };

        // Constrain the offered algorithm set to what Microsoft.DevTunnels.Ssh 3.x
        // actually supports (matched against `ssh -vv` against the same server). The
        // SSH.NET defaults include curve25519 and chacha20-poly1305, both of which
        // the server rejects; pruning them avoids unnecessary negotiation work and
        // keeps the client preference order aligned with what OpenSSH picked
        // (ecdh-sha2-nistp256 + aes256-ctr + hmac-sha2-256-etm).
        foreach (var name in info.KeyExchangeAlgorithms.Keys.Where(k => k.StartsWith("curve25519", StringComparison.Ordinal)).ToList())
        {
            info.KeyExchangeAlgorithms.Remove(name);
        }
        foreach (var name in info.Encryptions.Keys.Where(k => k.StartsWith("chacha20", StringComparison.Ordinal)).ToList())
        {
            info.Encryptions.Remove(name);
        }

        _client = new SshClient(info);
        await _client.ConnectAsync(ct).ConfigureAwait(false);

        // 120x40 PTY: wide enough that chat lines + handle prefixes don't wrap mid-message,
        // tall enough the lobby's art + welcome + carousel all paint above the fold.
        _stream = _client.CreateShellStream("xterm-256color", 120, 40, 1024, 768, 8192);
        _screen = new ScreenBuffer(_stream);
        _screen.Start();
    }

    public Task SendAsync(string text)
    {
        EnsureConnected();
        var bytes = Encoding.UTF8.GetBytes(text);
        return _stream!.WriteAsync(bytes, 0, bytes.Length, CancellationToken.None);
    }

    // Carriage return matches what xterm/PuTTY send for Enter and what Terminal.Gui v2
    // expects. Newline alone would not trigger button activation in some screens.
    public Task SendEnterAsync() => SendAsync("\r");

    public Task SendEscAsync() => SendAsync("\x1b");

    public Task SendKeyAsync(char key) => SendAsync(key.ToString());

    private void EnsureConnected()
    {
        if (_stream is null) throw new InvalidOperationException("Bot.ConnectAsync hasn't completed.");
    }

    public async ValueTask DisposeAsync()
    {
        try
        {
            if (_screen is not null) await _screen.DisposeAsync().ConfigureAwait(false);
        }
        catch { /* best effort */ }
        try
        {
            _stream?.Dispose();
        }
        catch { /* best effort */ }
        try
        {
            if (_client is { IsConnected: true })
            {
                _client.Disconnect();
            }
            _client?.Dispose();
        }
        catch { /* best effort */ }
    }
}
