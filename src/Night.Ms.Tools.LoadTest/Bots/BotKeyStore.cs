namespace Night.Ms.Tools.LoadTest.Bots;

// On-disk persistence for bot keypairs. Each bot's private key lives at `{root}/loadbot-NNNN.pem`
// as a PKCS#8 PEM (Renci.SshNet.PrivateKeyFile reads this format directly). Persisting makes
// load-test runs reproducible — re-seeding the database is idempotent, and re-running the test
// reuses the same fingerprints without churning identity_credentials rows.
public sealed class BotKeyStore
{
    private readonly string _root;

    public BotKeyStore(string root)
    {
        _root = root;
        Directory.CreateDirectory(_root);
    }

    public string HandleFor(int botIndex) => $"loadbot-{botIndex:D4}";

    public string PathFor(int botIndex) => Path.Combine(_root, $"{HandleFor(botIndex)}.pem");

    public BotKey LoadOrGenerate(int botIndex)
    {
        var path = PathFor(botIndex);
        if (File.Exists(path))
        {
            return BotKey.FromPkcs8Pem(File.ReadAllText(path));
        }
        var key = BotKey.Generate();
        File.WriteAllText(path, key.ExportPkcs8Pem());
        return key;
    }
}
