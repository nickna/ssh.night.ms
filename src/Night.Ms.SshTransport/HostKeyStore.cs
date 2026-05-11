using Microsoft.DevTunnels.Ssh;
using Microsoft.DevTunnels.Ssh.Algorithms;
using Microsoft.Extensions.Logging;

namespace Night.Ms.SshTransport;

public static class HostKeyStore
{
    public static IReadOnlyList<IKeyPair> GenerateEphemeralHostKeys(ILogger logger)
    {
        // v1: regenerate host keys on every startup. M10 polish will persist them under
        // a configurable directory so clients don't see "REMOTE HOST IDENTIFICATION HAS CHANGED".
        var rsa = SshAlgorithms.PublicKey.RsaWithSha512.GenerateKeyPair();
        var ecdsa = SshAlgorithms.PublicKey.ECDsaSha2Nistp256.GenerateKeyPair();

        logger.LogWarning(
            "Using ephemeral host keys (regenerated on each startup). " +
            "Clients will see host key change warnings between restarts. Persistence comes in M10.");

        return [rsa, ecdsa];
    }
}
