using System.Text;
using Microsoft.DevTunnels.Ssh.Algorithms;
using Microsoft.DevTunnels.Ssh.IO;
using Org.BouncyCastle.Crypto.Parameters;
using Org.BouncyCastle.Crypto.Signers;
using Org.BouncyCastle.Security;
using Buffer = Microsoft.DevTunnels.Ssh.Buffer;

namespace Night.Ms.SshTransport.Crypto;

// SSH ed25519 (RFC 8709). Ships nowhere in DevTunnels' SshAlgorithms; we register this
// instance into SshSessionConfiguration.PublicKeyAlgorithms ourselves.
//
// Wire format:
//   public key blob: string "ssh-ed25519" || string raw-pubkey-bytes(32)
//   signature blob:  string "ssh-ed25519" || string raw-signature-bytes(64)
// The base PublicKeyAlgorithm class handles signature framing via Read/CreateSignatureData
// because we pass the matching algorithm name in the constructor.
public sealed class Ed25519PublicKeyAlgorithm : PublicKeyAlgorithm
{
    public const string AlgorithmName = "ssh-ed25519";

    public Ed25519PublicKeyAlgorithm() : base(AlgorithmName, AlgorithmName, hashAlgorithmName: "SHA512") { }

    public override IKeyPair CreateKeyPair() => new Ed25519KeyPair();

    public override IKeyPair GenerateKeyPair(int? keySizeInBits = null)
    {
        var pair = new Ed25519KeyPair();
        var random = new SecureRandom();
        var priv = new Ed25519PrivateKeyParameters(random);
        pair.Import(priv, priv.GeneratePublicKey());
        return pair;
    }

    public override ISigner CreateSigner(IKeyPair keyPair)
    {
        if (keyPair is not Ed25519KeyPair ed) throw new ArgumentException("Expected Ed25519KeyPair", nameof(keyPair));
        if (!ed.HasPrivateKey) throw new InvalidOperationException("Cannot sign without a private key.");
        return new Ed25519SignerVerifier(ed);
    }

    public override IVerifier CreateVerifier(IKeyPair keyPair)
    {
        if (keyPair is not Ed25519KeyPair ed) throw new ArgumentException("Expected Ed25519KeyPair", nameof(keyPair));
        return new Ed25519SignerVerifier(ed);
    }
}

public sealed class Ed25519KeyPair : IKeyPair
{
    private const int PublicKeyByteLength = 32;
    private Ed25519PublicKeyParameters? _publicKey;
    private Ed25519PrivateKeyParameters? _privateKey;

    public string KeyAlgorithmName { get; } = Ed25519PublicKeyAlgorithm.AlgorithmName;
    public bool HasPrivateKey => _privateKey is not null;
    public string? Comment { get; set; }

    internal Ed25519PublicKeyParameters? PublicKey => _publicKey;
    internal Ed25519PrivateKeyParameters? PrivateKey => _privateKey;

    internal void Import(Ed25519PrivateKeyParameters priv, Ed25519PublicKeyParameters pub)
    {
        _privateKey = priv;
        _publicKey = pub;
    }

    public void SetPublicKeyBytes(Buffer keyBytes)
    {
        var reader = new SshDataReader(keyBytes);
        var algorithmName = reader.ReadString(Encoding.ASCII);
        if (algorithmName != Ed25519PublicKeyAlgorithm.AlgorithmName)
        {
            throw new ArgumentException($"Public key algorithm '{algorithmName}' does not match expected '{Ed25519PublicKeyAlgorithm.AlgorithmName}'.");
        }

        var raw = reader.ReadBinary();
        var rawBytes = raw.ToArray();
        if (rawBytes.Length != PublicKeyByteLength)
        {
            throw new ArgumentException($"ed25519 public key must be {PublicKeyByteLength} bytes, got {rawBytes.Length}.");
        }

        _publicKey = new Ed25519PublicKeyParameters(rawBytes, 0);
        // Importing only public key clears any previously held private key.
        _privateKey = null;
    }

    public Buffer GetPublicKeyBytes(string? algorithmName = null)
    {
        if (_publicKey is null) throw new InvalidOperationException("No public key set.");
        var name = algorithmName ?? KeyAlgorithmName;
        if (name != Ed25519PublicKeyAlgorithm.AlgorithmName)
        {
            throw new ArgumentException($"Algorithm '{name}' is not supported by this key pair.");
        }

        var raw = _publicKey.GetEncoded();
        var writer = new SshDataWriter();
        writer.Write(name, Encoding.ASCII);
        writer.WriteBinary((Buffer)raw);
        return writer.ToBuffer();
    }

    public void Dispose()
    {
        // Bouncy Castle parameter objects don't hold unmanaged state.
        _publicKey = null;
        _privateKey = null;
    }
}

internal sealed class Ed25519SignerVerifier : ISigner, IVerifier
{
    private readonly Ed25519KeyPair _keyPair;

    // Ed25519 always produces a 64-byte signature.
    public int DigestLength => 64;

    public Ed25519SignerVerifier(Ed25519KeyPair keyPair)
    {
        _keyPair = keyPair;
    }

    public void Sign(Buffer data, Buffer signature)
    {
        if (_keyPair.PrivateKey is null) throw new InvalidOperationException("Cannot sign without a private key.");
        if (signature.Count < DigestLength) throw new ArgumentException($"Signature buffer must be at least {DigestLength} bytes.", nameof(signature));

        var signer = new Ed25519Signer();
        signer.Init(forSigning: true, _keyPair.PrivateKey);
        var dataBytes = data.ToArray();
        signer.BlockUpdate(dataBytes, 0, dataBytes.Length);
        var sig = signer.GenerateSignature();
        sig.AsSpan().CopyTo(signature.Array.AsSpan(signature.Offset, signature.Count));
    }

    public bool Verify(Buffer data, Buffer signature)
    {
        if (_keyPair.PublicKey is null) return false;
        if (signature.Count != DigestLength) return false;

        var verifier = new Ed25519Signer();
        verifier.Init(forSigning: false, _keyPair.PublicKey);
        var dataBytes = data.ToArray();
        verifier.BlockUpdate(dataBytes, 0, dataBytes.Length);
        return verifier.VerifySignature(signature.ToArray());
    }

    public void Dispose() { }
}
