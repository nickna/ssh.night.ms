using Night.Ms.SshServer.Auth;

namespace Night.Ms.SshServer.Tests;

public class OpenSshPublicKeyParserTests
{
    // A known ed25519 public key in OpenSSH format (generated for the test, not from any
    // real user). Lets us assert that the parser produces the exact SHA256 fingerprint
    // that BbsSshServer's ComputeFingerprint derives from the wire-format blob.
    private const string Ed25519KeyText =
        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINeR3v1J6QTeCkbiHcEt6JMcLAYHM3sFnVUmZWcREumQ test-comment";

    [Fact]
    public void Parses_well_formed_ed25519_key()
    {
        Assert.True(OpenSshPublicKeyParser.TryParse(Ed25519KeyText, out var parsed));
        Assert.Equal("ssh-ed25519", parsed.Algorithm);
        Assert.Equal("test-comment", parsed.Comment);
        Assert.StartsWith("SHA256:", parsed.Fingerprint);
        // 32-byte ed25519 key + length-prefixed type tag = 51 bytes total.
        Assert.Equal(51, parsed.Blob.Length);
    }

    [Fact]
    public void Rejects_unknown_algorithm()
    {
        Assert.False(OpenSshPublicKeyParser.TryParse("ssh-bogus AAAA fake-key", out _));
    }

    [Fact]
    public void Rejects_empty_or_whitespace()
    {
        Assert.False(OpenSshPublicKeyParser.TryParse("", out _));
        Assert.False(OpenSshPublicKeyParser.TryParse("   ", out _));
        Assert.False(OpenSshPublicKeyParser.TryParse(null!, out _));
    }

    [Fact]
    public void Rejects_malformed_base64()
    {
        Assert.False(OpenSshPublicKeyParser.TryParse("ssh-ed25519 not-base64!@#", out _));
    }

    [Fact]
    public void Rejects_blob_with_mismatched_embedded_type()
    {
        // Base64 of "ssh-foo" — the embedded type "ssh-foo" doesn't match the declared
        // "ssh-ed25519", so the parser refuses it.
        var bogus = "ssh-ed25519 AAAABnNzaC1mb28=";
        Assert.False(OpenSshPublicKeyParser.TryParse(bogus, out _));
    }

    [Fact]
    public void Comment_is_optional()
    {
        var noComment = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINeR3v1J6QTeCkbiHcEt6JMcLAYHM3sFnVUmZWcREumQ";
        Assert.True(OpenSshPublicKeyParser.TryParse(noComment, out var parsed));
        Assert.Null(parsed.Comment);
    }

    [Fact]
    public void Trims_surrounding_whitespace()
    {
        var wrapped = "  " + Ed25519KeyText + "\n";
        Assert.True(OpenSshPublicKeyParser.TryParse(wrapped, out _));
    }

    [Fact]
    public void Fingerprint_is_stable_across_invocations()
    {
        Assert.True(OpenSshPublicKeyParser.TryParse(Ed25519KeyText, out var a));
        Assert.True(OpenSshPublicKeyParser.TryParse(Ed25519KeyText, out var b));
        Assert.Equal(a.Fingerprint, b.Fingerprint);
    }
}
