using System.Text;
using Microsoft.DevTunnels.Ssh.IO;
using Night.Ms.SshTransport.Crypto;
using Buffer = Microsoft.DevTunnels.Ssh.Buffer;

namespace Night.Ms.SshTransport.Tests;

public class Ed25519Tests
{
    private static Ed25519PublicKeyAlgorithm NewAlgorithm() => new();

    [Fact]
    public void GenerateKeyPair_yields_keypair_with_private_key()
    {
        var algo = NewAlgorithm();
        var pair = algo.GenerateKeyPair();
        Assert.IsType<Ed25519KeyPair>(pair);
        Assert.True(pair.HasPrivateKey);
        Assert.Equal(Ed25519PublicKeyAlgorithm.AlgorithmName, pair.KeyAlgorithmName);
    }

    [Fact]
    public void GetPublicKeyBytes_round_trips_through_SetPublicKeyBytes()
    {
        var algo = NewAlgorithm();
        var original = (Ed25519KeyPair)algo.GenerateKeyPair();

        var blob = original.GetPublicKeyBytes();

        var reimported = (Ed25519KeyPair)algo.CreateKeyPair();
        reimported.SetPublicKeyBytes(blob);

        Assert.False(reimported.HasPrivateKey);
        Assert.Equal(original.GetPublicKeyBytes().ToArray(), reimported.GetPublicKeyBytes().ToArray());
    }

    [Fact]
    public void SetPublicKeyBytes_rejects_wrong_algorithm_prefix()
    {
        var algo = NewAlgorithm();
        var pair = (Ed25519KeyPair)algo.CreateKeyPair();

        // Wrong algorithm name, then 32-byte placeholder.
        var writer = new SshDataWriter();
        writer.Write("ssh-rsa", Encoding.ASCII);
        writer.WriteBinary((Buffer)new byte[32]);
        var blob = writer.ToBuffer();

        var ex = Assert.Throws<ArgumentException>(() => pair.SetPublicKeyBytes(blob));
        Assert.Contains("ssh-ed25519", ex.Message);
    }

    [Fact]
    public void SetPublicKeyBytes_rejects_wrong_length()
    {
        var algo = NewAlgorithm();
        var pair = (Ed25519KeyPair)algo.CreateKeyPair();

        var writer = new SshDataWriter();
        writer.Write(Ed25519PublicKeyAlgorithm.AlgorithmName, Encoding.ASCII);
        writer.WriteBinary((Buffer)new byte[16]); // wrong: ed25519 keys are 32 bytes
        var blob = writer.ToBuffer();

        var ex = Assert.Throws<ArgumentException>(() => pair.SetPublicKeyBytes(blob));
        Assert.Contains("32 bytes", ex.Message);
    }

    [Fact]
    public void Signer_and_verifier_round_trip_against_each_other()
    {
        var algo = NewAlgorithm();
        var pair = algo.GenerateKeyPair();
        var data = (Buffer)Encoding.UTF8.GetBytes("the quick brown fox jumps over the lazy dog");

        using var signer = algo.CreateSigner(pair);
        var signature = new Buffer(signer.DigestLength);
        signer.Sign(data, signature);

        using var verifier = algo.CreateVerifier(pair);
        Assert.True(verifier.Verify(data, signature));
    }

    [Fact]
    public void Verify_returns_false_when_data_is_tampered()
    {
        var algo = NewAlgorithm();
        var pair = algo.GenerateKeyPair();
        var data = (Buffer)Encoding.UTF8.GetBytes("original payload");

        using var signer = algo.CreateSigner(pair);
        var signature = new Buffer(signer.DigestLength);
        signer.Sign(data, signature);

        var tampered = (Buffer)Encoding.UTF8.GetBytes("modified payload");
        using var verifier = algo.CreateVerifier(pair);
        Assert.False(verifier.Verify(tampered, signature));
    }

    [Fact]
    public void Verify_returns_false_when_signature_is_tampered()
    {
        var algo = NewAlgorithm();
        var pair = algo.GenerateKeyPair();
        var data = (Buffer)Encoding.UTF8.GetBytes("payload");

        using var signer = algo.CreateSigner(pair);
        var signature = new Buffer(signer.DigestLength);
        signer.Sign(data, signature);

        signature.Array[signature.Offset] ^= 0xFF; // flip a bit

        using var verifier = algo.CreateVerifier(pair);
        Assert.False(verifier.Verify(data, signature));
    }

    [Fact]
    public void Verify_returns_false_when_a_different_key_signed()
    {
        var algo = NewAlgorithm();
        var signingPair = algo.GenerateKeyPair();
        var otherPair = algo.GenerateKeyPair();
        var data = (Buffer)Encoding.UTF8.GetBytes("payload");

        using var signer = algo.CreateSigner(signingPair);
        var signature = new Buffer(signer.DigestLength);
        signer.Sign(data, signature);

        using var verifier = algo.CreateVerifier(otherPair);
        Assert.False(verifier.Verify(data, signature));
    }

    [Fact]
    public void CreateSigner_throws_when_keypair_lacks_private_key()
    {
        var algo = NewAlgorithm();
        var pair = (Ed25519KeyPair)algo.GenerateKeyPair();

        // Round-trip the public key into a fresh keypair (which then lacks the private half).
        var publicOnly = (Ed25519KeyPair)algo.CreateKeyPair();
        publicOnly.SetPublicKeyBytes(pair.GetPublicKeyBytes());

        Assert.Throws<InvalidOperationException>(() => algo.CreateSigner(publicOnly));
    }
}
