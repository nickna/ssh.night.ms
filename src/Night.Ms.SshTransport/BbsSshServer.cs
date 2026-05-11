using System.Collections.Concurrent;
using System.Diagnostics;
using System.Net;
using System.Security.Claims;
using System.Security.Cryptography;
using Microsoft.DevTunnels.Ssh;
using Microsoft.DevTunnels.Ssh.Algorithms;
using Microsoft.DevTunnels.Ssh.Events;
using Microsoft.DevTunnels.Ssh.Messages;
using Microsoft.Extensions.Logging;
using Night.Ms.SshTransport.Crypto;
using TcpSshServer = Microsoft.DevTunnels.Ssh.Tcp.SshServer;

namespace Night.Ms.SshTransport;

public sealed class BbsSshServer : IAsyncDisposable
{
    private readonly BbsSshServerOptions _options;
    private readonly ILogger<BbsSshServer> _logger;
    private readonly IReadOnlyList<IKeyPair> _hostKeys;
    private readonly ConcurrentDictionary<SshSession, PendingAuth> _pendingAuth = new();
    private TcpSshServer? _server;
    private Task? _acceptLoop;

    private sealed record PendingAuth(AuthDecision Decision, string Fingerprint, string KeyAlgorithm, byte[] PublicKeyBlob);

    public BbsSshServer(BbsSshServerOptions options, ILogger<BbsSshServer> logger)
    {
        _options = options;
        _logger = logger;
        _hostKeys = HostKeyStore.LoadOrGenerate(options.HostKeyDirectory, logger);
    }

    public event Func<BbsSession, CancellationToken, Task>? SessionStarted;

    public Task StartAsync(CancellationToken cancellationToken)
    {
        if (_server is not null) throw new InvalidOperationException("Already started.");

        var config = new SshSessionConfiguration(useSecurity: true);
        config.AuthenticationMethods.Clear();
        config.AuthenticationMethods.Add(AuthenticationMethods.PublicKey);

        // Register ed25519 client-key support. DevTunnels' KeyExchangeService composes the
        // exchange hash with all values (Q_C, Q_S, K) wrapped as bigints — that matches
        // ECDH P-curve points but breaks RFC 8731 which requires Q_C/Q_S as strings for
        // curve25519-sha256. Clients fall back to ecdh-sha2-nistp256 which works correctly.
        // Re-enabling curve25519-sha256 needs an upstream fix in DevTunnels.
        config.PublicKeyAlgorithms.Add(new Ed25519PublicKeyAlgorithm());
        // config.KeyExchangeAlgorithms.Add(new Curve25519KeyExchangeAlgorithm());

        var trace = new TraceSource(nameof(BbsSshServer));
        _server = new TcpSshServer(config, trace)
        {
            Credentials = new SshServerCredentials(_hostKeys.ToArray()),
        };

        _server.SessionAuthenticating += OnSessionAuthenticating;
        _server.ChannelOpening += OnChannelOpening;
        _server.ExceptionRaised += (_, ex) => _logger.LogError(ex, "Unhandled exception in SSH server");

        _logger.LogInformation("BbsSshServer starting on {Address}:{Port}", _options.ListenAddress, _options.Port);

        var localAddress = IPAddress.TryParse(_options.ListenAddress, out var addr) ? addr : IPAddress.Any;
        _acceptLoop = _server.AcceptSessionsAsync(_options.Port, localAddress);
        return Task.CompletedTask;
    }

    private void OnSessionAuthenticating(object? sender, SshAuthenticatingEventArgs e)
    {
        if (e.PublicKey is null)
        {
            _logger.LogDebug("Rejecting auth attempt: type={Type}", e.AuthenticationType);
            e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(null);
            return;
        }

        var fingerprint = ComputeFingerprint(e.PublicKey);
        var algorithm = e.PublicKey.KeyAlgorithmName;
        var publicKeyBlob = e.PublicKey.GetPublicKeyBytes().ToArray();

        if (e.AuthenticationType is not (SshAuthenticationType.ClientPublicKeyQuery or SshAuthenticationType.ClientPublicKey))
        {
            e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(null);
            return;
        }

        var session = sender as SshSession;
        e.AuthenticationTask = AuthenticateAsync(e, session, fingerprint, algorithm, publicKeyBlob);
    }

    private async Task<ClaimsPrincipal?> AuthenticateAsync(
        SshAuthenticatingEventArgs e,
        SshSession? session,
        string fingerprint,
        string algorithm,
        byte[] publicKeyBlob)
    {
        var query = new AuthQuery(fingerprint, algorithm, publicKeyBlob, e.Username);
        var cancellation = e.Cancellation;
        AuthDecision decision;
        try
        {
            decision = await _options.AuthLookup(query, cancellation).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Auth lookup failed for fingerprint={Fingerprint}", fingerprint);
            return null;
        }

        if (decision is AuthDecision.Banned banned)
        {
            _logger.LogWarning("Auth rejected (banned): fingerprint={Fingerprint} reason={Reason}", fingerprint, banned.Reason);
            return null;
        }

        if (e.AuthenticationType == SshAuthenticationType.ClientPublicKeyQuery)
        {
            // Phase 1 — return an UNauthenticated principal to signal "yes, this key is potentially
            // acceptable; please proceed to actually sign". We re-run the lookup in phase 2.
            _logger.LogDebug("Pubkey query OK: fingerprint={Fingerprint} decision={Decision}", fingerprint, decision.GetType().Name);
            return new ClaimsPrincipal(new ClaimsIdentity());
        }

        // Phase 2 — record decision so OnChannelOpening can attach it to the BbsSession.
        if (session is not null)
        {
            _pendingAuth[session] = new PendingAuth(decision, fingerprint, algorithm, publicKeyBlob);
        }

        var identity = new ClaimsIdentity(authenticationType: "ssh-publickey");
        identity.AddClaim(new Claim("ssh:fingerprint", fingerprint));
        identity.AddClaim(new Claim("ssh:algorithm", algorithm));
        if (decision is AuthDecision.Known known)
        {
            identity.AddClaim(new Claim(ClaimTypes.Name, known.Handle));
            identity.AddClaim(new Claim(ClaimTypes.NameIdentifier, known.UserId.ToString()));
            if (known.IsSysop) identity.AddClaim(new Claim(ClaimTypes.Role, "sysop"));
            _logger.LogInformation("Auth accepted (known): handle={Handle} fingerprint={Fingerprint}", known.Handle, fingerprint);
        }
        else
        {
            identity.AddClaim(new Claim(ClaimTypes.Name, e.Username ?? "guest"));
            _logger.LogInformation("Auth accepted (unknown — TOFU register flow): fingerprint={Fingerprint}", fingerprint);
        }

        return new ClaimsPrincipal(identity);
    }

    private void OnChannelOpening(object? sender, SshChannelOpeningEventArgs e)
    {
        if (!e.IsRemoteRequest) return;

        // Only session channels are accepted in v1. Port forwarding etc. is rejected.
        if (e.Channel.ChannelType != "session")
        {
            e.FailureReason = SshChannelOpenFailureReason.AdministrativelyProhibited;
            e.FailureDescription = "Only session channels are accepted on this server.";
            return;
        }

        var principal = e.Channel.Session.Principal as ClaimsPrincipal ?? new ClaimsPrincipal();
        _pendingAuth.TryRemove(e.Channel.Session, out var pending);
        var decision = pending?.Decision ?? AuthDecision.Unknown.Instance;
        var fingerprint = pending?.Fingerprint ?? principal.FindFirst("ssh:fingerprint")?.Value ?? "<unknown>";
        var algorithm = pending?.KeyAlgorithm ?? principal.FindFirst("ssh:algorithm")?.Value ?? "<unknown>";
        var publicKeyBlob = pending?.PublicKeyBlob ?? Array.Empty<byte>();

        _logger.LogDebug("Session channel opening for fingerprint={Fingerprint} decision={Decision}", fingerprint, decision.GetType().Name);

        var bbsSession = new BbsSession(e.Channel, principal, fingerprint, algorithm, publicKeyBlob, decision, pty: null);
        e.Channel.Request += (s, args) => HandleChannelRequest(bbsSession, args);
    }

    private void HandleChannelRequest(BbsSession session, SshRequestEventArgs<ChannelRequestMessage> args)
    {
        switch (args.RequestType)
        {
            case ChannelRequestTypes.Terminal:
                if (args.Request is TerminalRequestMessage term)
                {
                    session.Pty = new PtyInfo(term.Term ?? "xterm-256color", term.Columns, term.Rows, term.PixelWidth, term.PixelHeight);
                    _logger.LogDebug("PTY allocated: term={Term} cols={Cols} rows={Rows}", term.Term, term.Columns, term.Rows);
                }
                args.IsAuthorized = true;
                break;

            case ChannelRequestTypes.Shell:
                _logger.LogInformation("Shell channel opened for fingerprint={Fingerprint}", session.Fingerprint);
                args.IsAuthorized = true;
                _ = Task.Run(() => RaiseSessionStartedAsync(session));
                break;

            case WindowChangeRequestMessage.RequestTypeName:
                try
                {
                    var wc = args.Request.ConvertTo<WindowChangeRequestMessage>();
                    var change = new WindowChange(wc.Columns, wc.Rows, wc.PixelWidth, wc.PixelHeight);
                    // Update the shared Pty record so SshChannelOutput.GetSize() returns the new
                    // dimensions on the next main-loop iteration. Reference assignment is atomic.
                    if (session.Pty is { } prev)
                    {
                        session.Pty = prev with
                        {
                            Cols = wc.Columns,
                            Rows = wc.Rows,
                            PixelWidth = wc.PixelWidth,
                            PixelHeight = wc.PixelHeight,
                        };
                    }
                    else
                    {
                        session.Pty = new PtyInfo("xterm-256color", wc.Columns, wc.Rows, wc.PixelWidth, wc.PixelHeight);
                    }
                    session.RaiseWindowChanged(change);
                    _logger.LogDebug("window-change: cols={Cols} rows={Rows}", wc.Columns, wc.Rows);
                }
                catch (Exception ex)
                {
                    _logger.LogWarning(ex, "Failed to parse window-change payload; ignoring.");
                }
                args.IsAuthorized = true;
                break;

            default:
                args.IsAuthorized = false;
                break;
        }
    }

    private async Task RaiseSessionStartedAsync(BbsSession session)
    {
        try
        {
            var handler = SessionStarted;
            if (handler is null)
            {
                _logger.LogWarning("Shell channel opened but no SessionStarted handler is registered; closing.");
                await session.CloseAsync();
                return;
            }
            await handler(session, CancellationToken.None);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Session handler failed; closing channel.");
            try { await session.CloseAsync(); } catch { /* best-effort */ }
        }
    }

    private static string ComputeFingerprint(IKeyPair key)
    {
        var bytes = key.GetPublicKeyBytes().ToArray();
        var hash = SHA256.HashData(bytes);
        return "SHA256:" + Convert.ToBase64String(hash).TrimEnd('=');
    }

    public async ValueTask DisposeAsync()
    {
        if (_server is null) return;
        try
        {
            _server.Dispose();
            if (_acceptLoop is not null)
            {
                try { await _acceptLoop.ConfigureAwait(false); }
                catch (Exception ex) when (ex is ObjectDisposedException or OperationCanceledException) { /* expected on shutdown */ }
            }
        }
        finally
        {
            _server = null;
        }
    }
}
