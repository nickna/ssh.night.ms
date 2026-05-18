using Night.Ms.SshServer.Auth;
using Night.Ms.Tools.LoadTest.Bots;

namespace Night.Ms.Tools.LoadTest.Tests;

// Round-trip check: a freshly generated BotKey's public-key blob must parse cleanly through
// the production OpenSshPublicKeyParser and yield the same SHA256 fingerprint. If this drifts,
// every seeded bot's identity_credentials row will have a fingerprint the server doesn't
// recognize and auth will fail with no helpful error.
public class BotKeyTests
{
    [Fact]
    public void Generated_blob_round_trips_through_parser()
    {
        var key = BotKey.Generate();
        var oneLine = $"{key.Algorithm} {Convert.ToBase64String(key.PublicKeyBlob)} bot-test";

        Assert.True(OpenSshPublicKeyParser.TryParse(oneLine, out var parsed));
        Assert.Equal("ssh-rsa", parsed.Algorithm);
        Assert.Equal(key.Fingerprint, parsed.Fingerprint);
        Assert.Equal(key.PublicKeyBlob, parsed.Blob);
    }

    [Fact]
    public void Pem_round_trips()
    {
        var original = BotKey.Generate();
        var pem = original.ExportPrivateKeyPem();
        var restored = BotKey.FromPem(pem);
        Assert.Equal(original.Fingerprint, restored.Fingerprint);
        Assert.Equal(original.PublicKeyBlob, restored.PublicKeyBlob);
    }
}
