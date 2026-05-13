using Microsoft.DevTunnels.Ssh;
using Microsoft.DevTunnels.Ssh.Algorithms;
using Microsoft.DevTunnels.Ssh.Keys;
using Microsoft.Extensions.Logging;

namespace Night.Ms.SshTransport;

public static class HostKeyStore
{
    public static IReadOnlyList<IKeyPair> LoadOrGenerate(string? directory, ILogger logger)
    {
        if (string.IsNullOrEmpty(directory))
        {
            logger.LogWarning(
                "No host-key directory configured (ephemeral keys). Clients will see host-key-changed warnings between restarts. " +
                "Set BbsSshServerOptions.HostKeyDirectory (or NIGHTMS_HOST_KEY_DIR) to persist.");
            return GenerateEphemeral();
        }

        Directory.CreateDirectory(directory);
        var rsaPath = Path.Combine(directory, "ssh_host_rsa_key");
        var ecdsaPath = Path.Combine(directory, "ssh_host_ecdsa_key");

        var rsa = LoadOrCreate(rsaPath, () => SshAlgorithms.PublicKey.RsaWithSha512.GenerateKeyPair(), logger, "RSA");
        var ecdsa = LoadOrCreate(ecdsaPath, () => SshAlgorithms.PublicKey.ECDsaSha2Nistp256.GenerateKeyPair(), logger, "ECDSA");
        return [rsa, ecdsa];
    }

    private static IKeyPair LoadOrCreate(string path, Func<IKeyPair> generator, ILogger logger, string label)
    {
        if (File.Exists(path))
        {
            try
            {
                var key = KeyPair.ImportKeyFile(path, null, KeyFormat.OpenSsh, KeyEncoding.Default);
                logger.LogInformation("Loaded persistent host key: {Label} ({Path})", label, path);
                return key;
            }
            catch (Exception ex)
            {
                logger.LogWarning(ex, "Failed to load host key from {Path}; regenerating.", path);
            }
        }

        var generated = generator();
        try
        {
            KeyPair.ExportPrivateKeyFile(generated, path, passphrase: null, KeyFormat.OpenSsh, KeyEncoding.Default);
            if (!OperatingSystem.IsWindows())
            {
                File.SetUnixFileMode(path, UnixFileMode.UserRead | UnixFileMode.UserWrite);
            }
            logger.LogInformation("Generated and persisted host key: {Label} ({Path})", label, path);
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Generated host key but failed to persist to {Path} (continuing with in-memory key).", path);
        }
        return generated;
    }

    private static IReadOnlyList<IKeyPair> GenerateEphemeral()
    {
        return
        [
            SshAlgorithms.PublicKey.RsaWithSha512.GenerateKeyPair(),
            SshAlgorithms.PublicKey.ECDsaSha2Nistp256.GenerateKeyPair(),
        ];
    }
}
