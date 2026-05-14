using System.Net;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Tui;

// Wraps a BbsSession (the SSH transport's session object) so BbsSessionRunner can consume
// it through the transport-agnostic ITuiSession contract. The SSH-specific bits used by the
// TOFU RegisterScreen (key algorithm, fingerprint, public-key blob) are surfaced via the
// SshCredentialInputs accessor, which the runner only calls on the Unknown auth branch.
internal sealed class SshSessionAdapter(BbsSession session) : ITuiSession
{
    public BbsSession Inner => session;

    public AuthDecision AuthDecision => session.AuthDecision;
    public Stream Stream => session.Stream;
    public PtyInfo? Pty => session.Pty;
    public IPAddress? RemoteIPAddress => session.RemoteIPAddress;
    public string DisplayName => $"fp:{session.Fingerprint}";

    public SshCredentialInputs CredentialInputs => new(session.KeyAlgorithm, session.Fingerprint, session.PublicKeyBlob);
}

// Inputs the TOFU RegisterScreen needs to mint a new IdentityCredential row. Decoupling
// RegisterScreen from BbsSession keeps the screen reusable by anything that has these three
// values, and proves at the type level that web sessions (which have none of these) never
// reach the Unknown auth branch.
public readonly record struct SshCredentialInputs(string KeyAlgorithm, string Fingerprint, byte[] PublicKeyBlob);
