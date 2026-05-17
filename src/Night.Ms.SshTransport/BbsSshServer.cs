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

namespace Night.Ms.SshTransport;

public sealed class BbsSshServer : IAsyncDisposable
{
    private readonly BbsSshServerOptions _options;
    private readonly ILogger<BbsSshServer> _logger;
    private readonly IReadOnlyList<IKeyPair> _hostKeys;
    private readonly ConcurrentDictionary<SshSession, PendingAuth> _pendingAuth = new();
    // Peer IP captured by IpCapturingTcpSshServer at TCP accept, looked up at channel-open time.
    private readonly ConcurrentDictionary<SshSession, IPAddress> _remoteIPs = new();
    private IpCapturingTcpSshServer? _server;
    private Task? _acceptLoop;

    // Carries the auth decision plus any key the client OFFERED during this session — even
    // when that key didn't ultimately authenticate the session (e.g., user mistyped, server
    // didn't recognize the fingerprint, user fell through to password auth). The offered
    // values let the TUI prompt "adopt this key to your account?" after password login.
    // Fingerprint/KeyAlgorithm/PublicKeyBlob are the authenticating credential (empty when
    // auth happened via password); OfferedFingerprint/OfferedAlgorithm/OfferedBlob are
    // anything else the client showed us.
    private sealed record PendingAuth(
        AuthDecision Decision,
        string Fingerprint,
        string KeyAlgorithm,
        byte[] PublicKeyBlob,
        string? OfferedFingerprint = null,
        string? OfferedAlgorithm = null,
        byte[]? OfferedBlob = null);

    public BbsSshServer(BbsSshServerOptions options, ILogger<BbsSshServer> logger)
    {
        _options = options;
        _logger = logger;
        _hostKeys = HostKeyStore.LoadOrGenerate(options.HostKeyDirectory, logger);
    }

    public event Func<BbsSession, CancellationToken, Task>? SessionStarted;

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        if (_server is not null) throw new InvalidOperationException("Already started.");

        var config = new SshSessionConfiguration(useSecurity: true);
        config.AuthenticationMethods.Clear();
        // Order matters for OpenSSH-style clients: they try publickey first (offering
        // every key in the agent), then fall back to password if all fail. Putting Password
        // here advertises it on USERAUTH_FAILURE so the client knows it's an option without
        // having to probe "none". MaxClientAuthenticationAttempts is bumped from the default
        // because clients with several agent keys naturally exceed the default-5 limit
        // before falling through to password.
        config.AuthenticationMethods.Add(AuthenticationMethods.PublicKey);
        config.AuthenticationMethods.Add(AuthenticationMethods.Password);
        config.MaxClientAuthenticationAttempts = 8;

        // Register ed25519 client-key support. Curve25519-sha256 key exchange is gated behind
        // an opt-in flag because DevTunnels' KeyExchangeService composes the exchange hash
        // with all values wrapped as bigints — breaks RFC 8731 for X25519. See
        // BbsSshServerOptions.EnableCurve25519KeyExchange for the full caveat.
        config.PublicKeyAlgorithms.Add(new Ed25519PublicKeyAlgorithm());
        if (_options.EnableCurve25519KeyExchange)
        {
            config.KeyExchangeAlgorithms.Add(new Curve25519KeyExchangeAlgorithm());
        }

        var trace = new TraceSource(nameof(BbsSshServer));
        _server = new IpCapturingTcpSshServer(config, trace, _logger)
        {
            Credentials = new SshServerCredentials(_hostKeys.ToArray()),
        };

        _server.SessionOpened += OnSessionOpened;
        _server.SessionAuthenticating += OnSessionAuthenticating;
        _server.ChannelOpening += OnChannelOpening;
        _server.ExceptionRaised += (_, ex) => _logger.LogError(ex, "Unhandled exception in SSH server");

        _logger.LogInformation("BbsSshServer starting on {Address}:{Port}", _options.ListenAddress, _options.Port);

        var localAddress = IPAddress.TryParse(_options.ListenAddress, out var addr) ? addr : IPAddress.Any;
        _acceptLoop = _server.AcceptSessionsAsync(_options.Port, localAddress);

        // AcceptSessionsAsync runs forever in the success case — it only completes (faulted) on
        // bind errors like AddressAlreadyInUse. Give it a short window to surface those before
        // returning success, so a port collision is loud instead of silent.
        var settled = await Task.WhenAny(_acceptLoop, Task.Delay(250, cancellationToken)).ConfigureAwait(false);
        if (settled == _acceptLoop)
        {
            // Re-throw the underlying exception (AddressAlreadyInUse, AccessDenied, etc.).
            await _acceptLoop.ConfigureAwait(false);
        }
    }

    private void OnSessionOpened(object? sender, SshServerSession session)
    {
        // Fires synchronously after IpCapturingTcpSshServer.AcceptConnectionAsync returns and
        // before the session begins its handshake — pair the latest pending endpoint with it.
        var endpoint = _server?.ConsumePendingRemoteEndpoint();
        if (endpoint?.Address is { } ip)
        {
            _remoteIPs[session] = ip;
            _logger.LogDebug("Session opened from {Address}:{Port}", ip, endpoint.Port);
        }
        session.Closed += (_, _) =>
        {
            _remoteIPs.TryRemove(session, out _);
            // A session that authenticates but never opens a channel (port-scan, half-open)
            // would otherwise retain its PendingAuth row — including the public-key blob —
            // for the lifetime of the server. OnChannelOpening also TryRemoves; this is the
            // safety net for the no-channel path.
            _pendingAuth.TryRemove(session, out _);
        };
    }

    private void OnSessionAuthenticating(object? sender, SshAuthenticatingEventArgs e)
    {
        var session = sender as SshSession;
        var handle = (e.Username ?? string.Empty).Trim();
        _remoteIPs.TryGetValue(session!, out var sourceIp);

        switch (e.AuthenticationType)
        {
            case SshAuthenticationType.ClientPublicKeyQuery when e.PublicKey is not null:
            {
                var fingerprint = ComputeFingerprint(e.PublicKey);
                var algorithm = e.PublicKey.KeyAlgorithmName;
                var blob = e.PublicKey.GetPublicKeyBytes().ToArray();
                var query = new AuthQuery.PublicKeyQuery(handle, fingerprint, algorithm, blob, sourceIp);
                e.AuthenticationTask = HandlePublicKeyQueryAsync(query, e.Cancellation);
                break;
            }

            case SshAuthenticationType.ClientPublicKey when e.PublicKey is not null:
            {
                var fingerprint = ComputeFingerprint(e.PublicKey);
                var algorithm = e.PublicKey.KeyAlgorithmName;
                var blob = e.PublicKey.GetPublicKeyBytes().ToArray();
                var query = new AuthQuery.PublicKey(handle, fingerprint, algorithm, blob, sourceIp);
                e.AuthenticationTask = HandlePublicKeyAsync(query, session, e.Cancellation);
                break;
            }

            case SshAuthenticationType.ClientPassword:
            {
                var secret = e.Password ?? string.Empty;
                var query = new AuthQuery.Password(handle, secret, sourceIp);
                e.AuthenticationTask = HandlePasswordAsync(query, session, e.Cancellation);
                break;
            }

            case SshAuthenticationType.ClientNone:
            {
                // OpenSSH usually probes "none" first to discover methods; DevTunnels also
                // surfaces this here when AuthenticationMethods includes "none". We don't
                // advertise "none", but if it arrives we route to signup for unknown handles
                // so a brand-new user with no key in their agent can still on-ramp.
                var query = new AuthQuery.None(handle, sourceIp);
                e.AuthenticationTask = HandleNoneAsync(query, session, e.Cancellation);
                break;
            }

            default:
                _logger.LogDebug("Rejecting auth attempt: type={Type} handle={Handle}", e.AuthenticationType, handle);
                e.AuthenticationTask = Task.FromResult<ClaimsPrincipal?>(null);
                break;
        }
    }

    // Phase 1 of publickey auth — the client asks "would you accept this key?" before
    // signing. Returning an unauthenticated ClaimsPrincipal signals "yes, probably; please
    // sign and we'll verify". Returning null causes the client to advance to the next key
    // (or to the next auth method). SignupRequired is also acceptable at this phase — it
    // means "we'd let this key in as part of signup", which the client treats the same as
    // "yes, sign and we'll verify."
    private async Task<ClaimsPrincipal?> HandlePublicKeyQueryAsync(AuthQuery.PublicKeyQuery query, CancellationToken ct)
    {
        AuthDecision decision;
        try { decision = await _options.AuthLookup(query, ct).ConfigureAwait(false); }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Auth lookup (PublicKeyQuery) failed for fingerprint={Fingerprint}", query.Fingerprint);
            return null;
        }

        return decision switch
        {
            AuthDecision.Known or AuthDecision.SignupRequired => new ClaimsPrincipal(new ClaimsIdentity()),
            _ => null,
        };
    }

    private async Task<ClaimsPrincipal?> HandlePublicKeyAsync(AuthQuery.PublicKey query, SshSession? session, CancellationToken ct)
    {
        AuthDecision decision;
        try { decision = await _options.AuthLookup(query, ct).ConfigureAwait(false); }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Auth lookup (PublicKey) failed for fingerprint={Fingerprint}", query.Fingerprint);
            return null;
        }

        return decision switch
        {
            AuthDecision.Known k => StashAndBuildPrincipal(session, decision, query.Fingerprint, query.Algorithm, query.Blob, offeredFp: null, offeredAlgo: null, offeredBlob: null, knownHandle: k.Handle, signupHandle: null, kind: "publickey", isSysop: k.IsSysop, userId: k.UserId),
            // Signup via publickey: the user is connecting as an unknown handle with a key
            // offered. Accept the auth (so they reach the TUI) and preserve the key blob as
            // OfferedFingerprint/etc so the signup screen can adopt it on creation.
            AuthDecision.SignupRequired s => StashAndBuildPrincipal(session, decision, fingerprint: "", algorithm: "", blob: [], offeredFp: query.Fingerprint, offeredAlgo: query.Algorithm, offeredBlob: query.Blob, knownHandle: null, signupHandle: s.Handle, kind: "publickey-signup", isSysop: false, userId: -1),
            AuthDecision.Banned b => RejectBanned(query.Handle, b.Reason),
            AuthDecision.RateLimited r => RejectRateLimited(query.Handle, r.RetryAfter),
            AuthDecision.Refused ref_ => RejectRefused(query.Handle, ref_.Reason, "publickey", session, fingerprint: query.Fingerprint, algorithm: query.Algorithm, blob: query.Blob),
            _ => null,
        };
    }

    private async Task<ClaimsPrincipal?> HandlePasswordAsync(AuthQuery.Password query, SshSession? session, CancellationToken ct)
    {
        AuthDecision decision;
        try { decision = await _options.AuthLookup(query, ct).ConfigureAwait(false); }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Auth lookup (Password) failed for handle={Handle}", query.Handle);
            return null;
        }

        // If the session already had a publickey attempt (failed because the key wasn't
        // registered), the pending row carries the offered key — preserve it across the
        // method switch so the post-login TUI can offer adoption.
        string? carriedOfferedFp = null, carriedOfferedAlgo = null;
        byte[]? carriedOfferedBlob = null;
        if (session is not null && _pendingAuth.TryGetValue(session, out var prior))
        {
            carriedOfferedFp = prior.OfferedFingerprint ?? (prior.Fingerprint.Length > 0 ? prior.Fingerprint : null);
            carriedOfferedAlgo = prior.OfferedAlgorithm ?? (prior.KeyAlgorithm.Length > 0 ? prior.KeyAlgorithm : null);
            carriedOfferedBlob = prior.OfferedBlob ?? (prior.PublicKeyBlob.Length > 0 ? prior.PublicKeyBlob : null);
        }

        return decision switch
        {
            AuthDecision.Known k => StashAndBuildPrincipal(session, k with { OfferedFingerprint = carriedOfferedFp, OfferedAlgorithm = carriedOfferedAlgo, OfferedBlob = carriedOfferedBlob }, fingerprint: "", algorithm: "", blob: [], offeredFp: carriedOfferedFp, offeredAlgo: carriedOfferedAlgo, offeredBlob: carriedOfferedBlob, knownHandle: k.Handle, signupHandle: null, kind: "password", isSysop: k.IsSysop, userId: k.UserId),
            AuthDecision.SignupRequired s => StashAndBuildPrincipal(session, s with { OfferedFingerprint = carriedOfferedFp, OfferedAlgorithm = carriedOfferedAlgo, OfferedBlob = carriedOfferedBlob }, fingerprint: "", algorithm: "", blob: [], offeredFp: carriedOfferedFp, offeredAlgo: carriedOfferedAlgo, offeredBlob: carriedOfferedBlob, knownHandle: null, signupHandle: s.Handle, kind: "password-signup", isSysop: false, userId: -1),
            AuthDecision.Banned b => RejectBanned(query.Handle, b.Reason),
            AuthDecision.RateLimited r => RejectRateLimited(query.Handle, r.RetryAfter),
            AuthDecision.Refused ref_ => RejectRefused(query.Handle, ref_.Reason, "password", session, fingerprint: "", algorithm: "", blob: []),
            _ => null,
        };
    }

    private async Task<ClaimsPrincipal?> HandleNoneAsync(AuthQuery.None query, SshSession? session, CancellationToken ct)
    {
        AuthDecision decision;
        try { decision = await _options.AuthLookup(query, ct).ConfigureAwait(false); }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Auth lookup (None) failed for handle={Handle}", query.Handle);
            return null;
        }

        return decision switch
        {
            // None auth never accepts a Known login (no credential was presented). The host
            // can still return SignupRequired here to let an unknown handle reach the TUI
            // with no key offered — they'll set a password during signup.
            AuthDecision.SignupRequired s => StashAndBuildPrincipal(session, decision, fingerprint: "", algorithm: "", blob: [], offeredFp: null, offeredAlgo: null, offeredBlob: null, knownHandle: null, signupHandle: s.Handle, kind: "none-signup", isSysop: false, userId: -1),
            AuthDecision.Banned b => RejectBanned(query.Handle, b.Reason),
            AuthDecision.RateLimited r => RejectRateLimited(query.Handle, r.RetryAfter),
            AuthDecision.Refused ref_ => RejectRefused(query.Handle, ref_.Reason, "none", session, fingerprint: "", algorithm: "", blob: []),
            _ => null,
        };
    }

    private ClaimsPrincipal StashAndBuildPrincipal(
        SshSession? session,
        AuthDecision decision,
        string fingerprint,
        string algorithm,
        byte[] blob,
        string? offeredFp,
        string? offeredAlgo,
        byte[]? offeredBlob,
        string? knownHandle,
        string? signupHandle,
        string kind,
        bool isSysop,
        long userId)
    {
        if (session is not null)
        {
            _pendingAuth[session] = new PendingAuth(decision, fingerprint, algorithm, blob, offeredFp, offeredAlgo, offeredBlob);
        }

        var identity = new ClaimsIdentity(authenticationType: $"ssh-{kind}");
        if (fingerprint.Length > 0) identity.AddClaim(new Claim("ssh:fingerprint", fingerprint));
        if (algorithm.Length > 0) identity.AddClaim(new Claim("ssh:algorithm", algorithm));
        if (knownHandle is not null)
        {
            identity.AddClaim(new Claim(ClaimTypes.Name, knownHandle));
            identity.AddClaim(new Claim(ClaimTypes.NameIdentifier, userId.ToString()));
            if (isSysop) identity.AddClaim(new Claim(ClaimTypes.Role, "sysop"));
            _logger.LogInformation("Auth accepted ({Kind}): handle={Handle} fp={Fingerprint}", kind, knownHandle, fingerprint.Length > 0 ? fingerprint : "(none)");
        }
        else
        {
            identity.AddClaim(new Claim(ClaimTypes.Name, signupHandle ?? "guest"));
            _logger.LogInformation("Auth accepted (signup, {Kind}): handle={Handle} offeredFp={OfferedFp}", kind, signupHandle, offeredFp ?? "(none)");
        }
        return new ClaimsPrincipal(identity);
    }

    private ClaimsPrincipal? RejectBanned(string handle, string reason)
    {
        _logger.LogWarning("Auth rejected (banned): handle={Handle} reason={Reason}", handle, reason);
        return null;
    }

    private ClaimsPrincipal? RejectRateLimited(string handle, TimeSpan retryAfter)
    {
        _logger.LogWarning("Auth rejected (rate-limited): handle={Handle} retryAfter={RetryAfter}", handle, retryAfter);
        return null;
    }

    // Refused = "this specific credential didn't authenticate; advertise other methods so
    // the client can try again." The pending row still stashes the offered key so a later
    // successful password attempt on the same session can carry it into the TUI for
    // adoption — that's the recover-from-unknown-key path.
    private ClaimsPrincipal? RejectRefused(string handle, string reason, string kind, SshSession? session, string fingerprint, string algorithm, byte[] blob)
    {
        _logger.LogInformation("Auth refused ({Kind}): handle={Handle} reason={Reason}", kind, handle, reason);
        if (session is not null && fingerprint.Length > 0)
        {
            // Preserve the offered key for a possible later password fallback on the same
            // SSH session. Without this, the adopt-key prompt after password login wouldn't
            // know about a key the user offered earlier.
            _pendingAuth[session] = new PendingAuth(
                Decision: new AuthDecision.Refused(reason),
                Fingerprint: "",
                KeyAlgorithm: "",
                PublicKeyBlob: [],
                OfferedFingerprint: fingerprint,
                OfferedAlgorithm: algorithm,
                OfferedBlob: blob);
        }
        return null;
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
        // No more AuthDecision.Unknown sentinel — a session that reached channel-open
        // without an auth decision is a logic bug somewhere, but tolerate it with a
        // SignupRequired ("guest") so we at least log something sensible.
        var decision = pending?.Decision ?? new AuthDecision.SignupRequired("guest");
        var fingerprint = pending?.Fingerprint ?? principal.FindFirst("ssh:fingerprint")?.Value ?? "";
        var algorithm = pending?.KeyAlgorithm ?? principal.FindFirst("ssh:algorithm")?.Value ?? "";
        var publicKeyBlob = pending?.PublicKeyBlob ?? [];

        _logger.LogDebug("Session channel opening for fingerprint={Fingerprint} decision={Decision}",
            fingerprint.Length > 0 ? fingerprint : "(none)", decision.GetType().Name);

        _remoteIPs.TryGetValue(e.Channel.Session, out var remoteIp);
        var bbsSession = new BbsSession(
            e.Channel, principal, fingerprint, algorithm, publicKeyBlob, decision,
            pty: null, remoteIPAddress: remoteIp,
            offeredFingerprint: pending?.OfferedFingerprint,
            offeredAlgorithm: pending?.OfferedAlgorithm,
            offeredBlob: pending?.OfferedBlob);
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
