using System.Security.Cryptography;
using Microsoft.DevTunnels.Ssh.Algorithms;
using Org.BouncyCastle.Crypto.Agreement;
using Org.BouncyCastle.Crypto.Parameters;
using Org.BouncyCastle.Security;
using Buffer = Microsoft.DevTunnels.Ssh.Buffer;

namespace Night.Ms.SshTransport.Crypto;

// SSH curve25519-sha256 KEX (RFC 8731). Not in DevTunnels' default SshAlgorithms;
// we register an instance into SshSessionConfiguration.KeyExchangeAlgorithms.
//
// Wire / hash composition is handled by KeyExchangeService:
//   - Our StartKeyExchange returns the server's raw 32-byte ephemeral X25519 public key.
//   - DecryptKeyExchange takes the client's raw 32-byte X25519 public key and returns
//     the raw shared secret bytes; the protocol layer wraps it as mpint when computing H.
//   - Sign(data, output) computes SHA-256(data) into output (the IKeyExchange hash function,
//     used to produce the exchange hash H that the host key ultimately signs).
public sealed class Curve25519KeyExchangeAlgorithm : KeyExchangeAlgorithm
{
    public const string AlgorithmName = "curve25519-sha256";

    public Curve25519KeyExchangeAlgorithm()
        : base(AlgorithmName, keySizeInBits: 256, hashAlgorithmName: "SHA256", hashDigestLength: 32) { }

    public override IKeyExchange CreateKeyExchange() => new Curve25519KeyExchange();
}

internal sealed class Curve25519KeyExchange : IKeyExchange
{
    private const int KeyByteLength = 32;
    private byte[]? _privateKey;
    private SHA256? _hash;

    public int DigestLength => 32;

    public Buffer StartKeyExchange()
    {
        var random = new SecureRandom();
        var priv = new X25519PrivateKeyParameters(random);
        _privateKey = priv.GetEncoded();
        return priv.GeneratePublicKey().GetEncoded();
    }

    public Buffer DecryptKeyExchange(Buffer exchangeValue)
    {
        if (_privateKey is null) throw new InvalidOperationException("StartKeyExchange must be called first.");
        var peerBytes = exchangeValue.ToArray();
        if (peerBytes.Length != KeyByteLength)
        {
            throw new ArgumentException($"X25519 public key must be {KeyByteLength} bytes, got {peerBytes.Length}.", nameof(exchangeValue));
        }

        var priv = new X25519PrivateKeyParameters(_privateKey, 0);
        var peer = new X25519PublicKeyParameters(peerBytes, 0);
        var agreement = new X25519Agreement();
        agreement.Init(priv);
        var shared = new byte[agreement.AgreementSize];
        agreement.CalculateAgreement(peer, shared, 0);
        return shared;
    }

    public void Sign(Buffer data, Buffer signature)
    {
        _hash ??= SHA256.Create();
        var hash = _hash.ComputeHash(data.Array, data.Offset, data.Count);
        hash.AsSpan().CopyTo(signature.Array.AsSpan(signature.Offset, DigestLength));
    }

    public void Dispose()
    {
        _hash?.Dispose();
        _hash = null;
        if (_privateKey is not null)
        {
            CryptographicOperations.ZeroMemory(_privateKey);
            _privateKey = null;
        }
    }
}
