using System.Net;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Tui;

// The slice of session state BbsSessionRunner actually needs. Both the SSH transport
// (via SshSessionAdapter) and the web WebSocket transport (via WebSocketTuiSession)
// satisfy this contract, so the runner stays unaware of which one it's wrapping.
public interface ITuiSession
{
    AuthDecision AuthDecision { get; }

    // Duplex byte stream the Terminal.Gui driver reads stdin from and writes ANSI output to.
    Stream Stream { get; }

    // Most-recent PTY size. Driver re-reads on every render tick, so resize messages take
    // effect on the next paint without a subscription. Null until the client has reported
    // an initial size (SSH pty-req or browser FitAddon resize).
    PtyInfo? Pty { get; }

    // Client IP recovered upstream (SSH peer or HTTP connection's remote address). May be
    // null for unusual transports.
    IPAddress? RemoteIPAddress { get; }

    // Identifier used only in log lines. SSH adapter formats it as "fp:<fingerprint>", the
    // web adapter as "ws:user-<id>". Not for authentication.
    string DisplayName { get; }
}
