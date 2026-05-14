using System.Net;
using System.Net.WebSockets;
using System.Security.Claims;
using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Tui;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Web;

// ITuiSession backed by a browser WebSocket. Auth is decided up-front from the cookie
// principal (the /ws/bbs endpoint is [Authorize]-gated); the underlying WebSocketDuplexStream
// owns the byte path and the mutable Pty driven by client-side resize messages.
public sealed class WebSocketTuiSession : ITuiSession, IAsyncDisposable
{
    private readonly WebSocketDuplexStream _stream;
    public AuthDecision AuthDecision { get; }
    public Stream Stream => _stream;
    public PtyInfo? Pty => _stream.Pty;
    public IPAddress? RemoteIPAddress { get; }
    public string DisplayName { get; }

    private WebSocketTuiSession(AuthDecision decision, WebSocketDuplexStream stream, IPAddress? remoteIp, string displayName)
    {
        AuthDecision = decision;
        _stream = stream;
        RemoteIPAddress = remoteIp;
        DisplayName = displayName;
    }

    // Builds the session from the just-accepted WebSocket. Returns null (after sending a
    // close frame) if the cookie principal can't be resolved to a non-banned User row —
    // callers don't need to do anything else in that case.
    public static async Task<WebSocketTuiSession?> CreateAsync(
        WebSocket ws,
        HttpContext httpContext,
        AppDbContext db,
        ILogger logger,
        CancellationToken cancellationToken)
    {
        var idStr = httpContext.User.FindFirstValue(ClaimTypes.NameIdentifier);
        if (!long.TryParse(idStr, out var userId))
        {
            await CloseQuietlyAsync(ws, "missing-identity");
            return null;
        }
        var user = await db.Users.AsNoTracking()
            .FirstOrDefaultAsync(u => u.Id == userId, cancellationToken);
        if (user is null)
        {
            logger.LogWarning("WS sign-in claim references missing user id={UserId}", userId);
            await CloseQuietlyAsync(ws, "no-user");
            return null;
        }
        if (user.IsBanned)
        {
            logger.LogInformation("Banned user attempted WS sign-in: handle={Handle}", user.Handle);
            await CloseQuietlyAsync(ws, "banned");
            return null;
        }

        // No initial PTY size — the browser's FitAddon will fire a resize message after
        // construction. Driver falls back to 80x24 (BbsSessionRunner.GetPtySize) until then.
        var stream = new WebSocketDuplexStream(ws, initialPty: null, logger: logger);
        var decision = new AuthDecision.Known(user.Id, user.Handle, user.IsSysop);
        var display = $"ws:user-{user.Id}";
        return new WebSocketTuiSession(decision, stream, httpContext.Connection.RemoteIpAddress, display);
    }

    private static async Task CloseQuietlyAsync(WebSocket ws, string reason)
    {
        try
        {
            if (ws.State == WebSocketState.Open)
            {
                await ws.CloseAsync(WebSocketCloseStatus.PolicyViolation, reason, CancellationToken.None);
            }
        }
        catch { /* peer might have already disappeared */ }
    }

    public ValueTask DisposeAsync() => _stream.DisposeAsync();
}
