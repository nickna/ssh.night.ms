using System.Diagnostics;
using System.Text;

namespace Night.Ms.Tools.LoadTest.Bots;

// One synthetic SSH session, implemented as a spawned `ssh.exe` subprocess (OpenSSH).
// We deliberately don't use Renci.SshNet here — its 2024.x line has strict-kex interop
// issues with Microsoft.DevTunnels.Ssh 3.x, and the 2023.x line drops PKCS#8 keys and
// may negotiate the legacy ssh-rsa (SHA-1) signature that the server rejects. OpenSSH
// ssh.exe has shipped with Windows since 1809, and we verified end-to-end auth + lobby
// rendering with `ssh -vv` against the same key during development.
//
// Cost: one OS process per bot, ~5 MB resident set apiece. At N=500 that's ~2.5 GB
// client-side — well within budget on a dev box, and we get bug-free transport
// in exchange for the memory.
public sealed class Bot : IAsyncDisposable
{
    private readonly string _host;
    private readonly int _port;
    private readonly string _handle;
    private readonly string _privateKeyPath;

    private Process? _proc;
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
        var psi = new ProcessStartInfo
        {
            FileName = "ssh",
            RedirectStandardInput = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
            CreateNoWindow = true,
        };
        // -tt forces PTY allocation even though our stdin is redirected — TG can't render
        // without one. UserKnownHostsFile=NUL (Windows) silences host-key prompts and
        // avoids writing into the user's real ~/.ssh/known_hosts.
        psi.ArgumentList.Add("-tt");
        psi.ArgumentList.Add("-p"); psi.ArgumentList.Add(_port.ToString());
        psi.ArgumentList.Add("-i"); psi.ArgumentList.Add(_privateKeyPath);
        psi.ArgumentList.Add("-o"); psi.ArgumentList.Add("StrictHostKeyChecking=accept-new");
        psi.ArgumentList.Add("-o"); psi.ArgumentList.Add("UserKnownHostsFile=NUL");
        psi.ArgumentList.Add("-o"); psi.ArgumentList.Add("BatchMode=yes");
        psi.ArgumentList.Add("-o"); psi.ArgumentList.Add("ConnectTimeout=15");
        psi.ArgumentList.Add("-o"); psi.ArgumentList.Add("LogLevel=ERROR");
        psi.ArgumentList.Add($"{_handle}@{_host}");

        // 256-color terminal so SGR escapes render cleanly. PTY dimensions are derived
        // by ssh.exe from its (non-existent) controlling terminal and default to 80x24
        // when there isn't one — fine for our screens, which target ≤ 80 cols anyway.
        psi.Environment["TERM"] = "xterm-256color";

        _proc = Process.Start(psi)
            ?? throw new InvalidOperationException($"Failed to start ssh.exe for {_handle}");

        _screen = new ScreenBuffer(_proc.StandardOutput.BaseStream);
        _screen.Start();

        // Drain stderr in the background. If ssh prints anything to stderr it's usually a
        // host-key warning or a real failure — surface real failures (non-zero exit) so
        // we don't silently swallow useful diagnostic info.
        _ = Task.Run(async () =>
        {
            try
            {
                var err = await _proc.StandardError.ReadToEndAsync().ConfigureAwait(false);
                if (_proc.HasExited && _proc.ExitCode != 0 && !string.IsNullOrWhiteSpace(err))
                {
                    Console.Error.WriteLine($"loadtest: bot {_handle} ssh stderr: {err.Trim()}");
                }
            }
            catch { /* best effort */ }
        });

        // Short post-spawn settling window. If ssh.exe exits within this window, the
        // connect attempt failed (auth refused, port unreachable, key rejected, etc.) —
        // throw so the Driver records it as a bot.connect error with the exit code as
        // the proximate signal.
        var settle = Task.Delay(TimeSpan.FromSeconds(2), ct);
        var earlyExit = _proc.WaitForExitAsync(ct);
        var first = await Task.WhenAny(settle, earlyExit).ConfigureAwait(false);
        if (first == earlyExit)
        {
            throw new InvalidOperationException(
                $"ssh.exe for {_handle} exited prematurely with code {_proc.ExitCode}");
        }
    }

    public Task SendAsync(string text)
    {
        EnsureConnected();
        var bytes = Encoding.UTF8.GetBytes(text);
        return _proc!.StandardInput.BaseStream.WriteAsync(bytes, 0, bytes.Length, CancellationToken.None);
    }

    public Task SendEnterAsync() => SendAsync("\r");
    public Task SendEscAsync() => SendAsync("\x1b");
    public Task SendKeyAsync(char key) => SendAsync(key.ToString());

    private void EnsureConnected()
    {
        if (_proc is null || _proc.HasExited) throw new InvalidOperationException("Bot is not connected.");
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
            if (_proc is not null && !_proc.HasExited)
            {
                // Ask the TUI to step back to lobby and quit, then close stdin so ssh.exe
                // sees EOF and shuts the channel. Hard-kill after a short grace period if
                // it's still alive.
                try { _proc.StandardInput.Write("\x1b\x1b\x1bL"); _proc.StandardInput.Close(); } catch { }
                if (!_proc.WaitForExit(500))
                {
                    try { _proc.Kill(entireProcessTree: false); } catch { /* already exited */ }
                }
            }
            _proc?.Dispose();
        }
        catch { /* best effort */ }
    }
}
