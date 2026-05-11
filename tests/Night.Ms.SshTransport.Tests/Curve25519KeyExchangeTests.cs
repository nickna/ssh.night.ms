using Microsoft.DevTunnels.Ssh.Algorithms;
using Night.Ms.SshTransport.Crypto;
using Org.BouncyCastle.Crypto.Agreement;
using Org.BouncyCastle.Crypto.Parameters;
using Org.BouncyCastle.Security;
using Buffer = Microsoft.DevTunnels.Ssh.Buffer;

namespace Night.Ms.SshTransport.Tests;

// These tests pin the X25519 primitive math even though the KEX is currently disabled in
// BbsSshServer (DevTunnels' KeyExchangeService bigint-encodes Q_C/Q_S where RFC 8731
// requires strings). When DevTunnels patches the hash composition we want these
// invariants already locked.
public class Curve25519KeyExchangeTests
{
    [Fact]
    public void StartKeyExchange_returns_32_byte_public_key()
    {
        IKeyExchange kex = new Curve25519KeyExchangeAlgorithm().CreateKeyExchange();
        var pub = kex.StartKeyExchange();
        Assert.Equal(32, pub.Count);
    }

    [Fact]
    public void DigestLength_is_32_bytes_for_SHA256()
    {
        IKeyExchange kex = new Curve25519KeyExchangeAlgorithm().CreateKeyExchange();
        Assert.Equal(32, kex.DigestLength);
    }

    [Fact]
    public void DecryptKeyExchange_throws_before_StartKeyExchange()
    {
        IKeyExchange kex = new Curve25519KeyExchangeAlgorithm().CreateKeyExchange();
        Assert.Throws<InvalidOperationException>(() => kex.DecryptKeyExchange((Buffer)new byte[32]));
    }

    [Fact]
    public void DecryptKeyExchange_rejects_wrong_length_peer_key()
    {
        IKeyExchange kex = new Curve25519KeyExchangeAlgorithm().CreateKeyExchange();
        kex.StartKeyExchange();
        Assert.Throws<ArgumentException>(() => kex.DecryptKeyExchange((Buffer)new byte[31]));
    }

    [Fact]
    public void DecryptKeyExchange_agrees_with_independent_X25519_peer()
    {
        // Server side — uses our wrapper.
        IKeyExchange kex = new Curve25519KeyExchangeAlgorithm().CreateKeyExchange();
        var serverPub = kex.StartKeyExchange().ToArray();

        // Peer side — pure BouncyCastle, mirroring what an OpenSSH client does.
        var peerPriv = new X25519PrivateKeyParameters(new SecureRandom());
        var peerPub = peerPriv.GeneratePublicKey().GetEncoded();

        // Each side computes the shared secret from the other side's public key.
        var serverShared = kex.DecryptKeyExchange((Buffer)peerPub).ToArray();

        var peerAgreement = new X25519Agreement();
        peerAgreement.Init(peerPriv);
        var peerShared = new byte[peerAgreement.AgreementSize];
        peerAgreement.CalculateAgreement(new X25519PublicKeyParameters(serverPub, 0), peerShared, 0);

        Assert.Equal(peerShared, serverShared);
    }

    [Fact]
    public void Sign_writes_SHA256_of_input_into_output_buffer()
    {
        IKeyExchange kex = new Curve25519KeyExchangeAlgorithm().CreateKeyExchange();
        var data = (Buffer)System.Text.Encoding.UTF8.GetBytes("the quick brown fox");
        var output = new Buffer(kex.DigestLength);
        kex.Sign(data, output);

        var expected = System.Security.Cryptography.SHA256.HashData(data.ToArray());
        Assert.Equal(expected, output.ToArray());
    }
}
