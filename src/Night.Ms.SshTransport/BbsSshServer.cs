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
    private TcpSshServer? _server;
    private Task? _acceptLoop;

    public BbsSshServer(BbsSshServerOptions options, ILogger<BbsSshServer> logger)
    {
        _options = options;
        _logger = logger;
        _hostKeys = HostKeyStore.GenerateEphemeralHostKeys(logger);
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
        // M2 placeholder: accept any client public key. M5 wires this to a fingerprint lookup
        // against the users database for the TOFU flow.
        if (e.PublicKey is null)
        {
            _logger.LogDebug("Rejecting auth attempt: type={Type}", e.AuthenticationType);
            e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(null);
            return;
        }

        var fingerprint = ComputeFingerprint(e.PublicKey);
        var algorithm = e.PublicKey.KeyAlgorithmName;

        if (e.AuthenticationType == SshAuthenticationType.ClientPublicKeyQuery)
        {
            // Phase 1: client asks "would this key be acceptable?". Return an UNauthenticated
            // principal to signal yes; the library will then prompt the client for a signature.
            _logger.LogDebug("Pubkey query: user={User} algorithm={Algorithm} fingerprint={Fingerprint}",
                e.Username, algorithm, fingerprint);
            e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(new ClaimsPrincipal(new ClaimsIdentity()));
            return;
        }

        if (e.AuthenticationType != SshAuthenticationType.ClientPublicKey)
        {
            e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(null);
            return;
        }

        // Phase 2: client provided signature; library has verified it. Return an authenticated principal.
        _logger.LogInformation("Auth accepted (placeholder): user={User} algorithm={Algorithm} fingerprint={Fingerprint}",
            e.Username, algorithm, fingerprint);

        var identity = new ClaimsIdentity(authenticationType: "ssh-publickey");
        identity.AddClaim(new Claim(ClaimTypes.Name, e.Username ?? "anonymous"));
        identity.AddClaim(new Claim("ssh:fingerprint", fingerprint));
        identity.AddClaim(new Claim("ssh:algorithm", algorithm));

        e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(new ClaimsPrincipal(identity));
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

        var principal = e.Channel.Session.Principal as ClaimsPrincipal;
        var fingerprint = principal?.FindFirst("ssh:fingerprint")?.Value ?? "<unknown>";
        var algorithm = principal?.FindFirst("ssh:algorithm")?.Value ?? "<unknown>";

        _logger.LogDebug("Session channel opening for fingerprint={Fingerprint}", fingerprint);

        var bbsSession = new BbsSession(e.Channel, principal ?? new ClaimsPrincipal(), fingerprint, algorithm, pty: null);
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

            // window-change has no typed message in DevTunnels; M4 will handle it when the
            // Terminal.Gui driver actually needs to react to resize.

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
