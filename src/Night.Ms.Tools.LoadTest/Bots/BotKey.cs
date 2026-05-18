using System.Buffers.Binary;
using System.Security.Cryptography;
using System.Text;

namespace Night.Ms.Tools.LoadTest.Bots;

// Generates an RSA-2048 keypair and renders it in the formats the seeder + SSH client need:
// (a) OpenSSH RFC 4253 §6.6 wire-format public-key blob, (b) the SHA256: fingerprint string
// matching what OpenSshPublicKeyParser.TryParse produces, and (c) the PKCS#8 PEM private-key
// text that Renci.SshNet's PrivateKeyFile accepts. RSA (not ed25519) is deliberate: bot key
// type doesn't affect the perf we're measuring, and Renci.SshNet's RSA support is solid where
// its ed25519 support has historically been patchy.
public sealed class BotKey
{
    public RSA Rsa { get; }
    public byte[] PublicKeyBlob { get; }
    public string Fingerprint { get; }
    public string Algorithm => "ssh-rsa";

    private BotKey(RSA rsa, byte[] blob, string fingerprint)
    {
        Rsa = rsa;
        PublicKeyBlob = blob;
        Fingerprint = fingerprint;
    }

    public static BotKey Generate()
    {
        var rsa = RSA.Create(2048);
        return Wrap(rsa);
    }

    public static BotKey FromPkcs8Pem(string pem)
    {
        var rsa = RSA.Create();
        rsa.ImportFromPem(pem);
        return Wrap(rsa);
    }

    public string ExportPkcs8Pem() => Rsa.ExportPkcs8PrivateKeyPem();

    private static BotKey Wrap(RSA rsa)
    {
        var p = rsa.ExportParameters(false);
        var blob = BuildOpenSshRsaBlob(p.Exponent!, p.Modulus!);
        var fingerprint = "SHA256:" + Convert.ToBase64String(SHA256.HashData(blob)).TrimEnd('=');
        return new BotKey(rsa, blob, fingerprint);
    }

    // OpenSSH wire format for an RSA public key:
    //   string  "ssh-rsa"
    //   mpint   e
    //   mpint   n
    // where `string` is 4-byte big-endian length + bytes, and `mpint` is the same plus a
    // signed-bigint convention: if the high bit of the first byte is set, prepend 0x00 so
    // the value isn't mistaken for negative.
    private static byte[] BuildOpenSshRsaBlob(byte[] exponent, byte[] modulus)
    {
        var typeBytes = Encoding.ASCII.GetBytes("ssh-rsa");
        var eBytes = ToMpint(exponent);
        var nBytes = ToMpint(modulus);

        var total = 4 + typeBytes.Length + 4 + eBytes.Length + 4 + nBytes.Length;
        var blob = new byte[total];
        var offset = 0;
        WriteLengthPrefixed(blob, ref offset, typeBytes);
        WriteLengthPrefixed(blob, ref offset, eBytes);
        WriteLengthPrefixed(blob, ref offset, nBytes);
        return blob;
    }

    private static byte[] ToMpint(byte[] unsignedBigEndian)
    {
        if (unsignedBigEndian.Length == 0) return unsignedBigEndian;
        if ((unsignedBigEndian[0] & 0x80) == 0) return unsignedBigEndian;
        var padded = new byte[unsignedBigEndian.Length + 1];
        Buffer.BlockCopy(unsignedBigEndian, 0, padded, 1, unsignedBigEndian.Length);
        return padded;
    }

    private static void WriteLengthPrefixed(byte[] dest, ref int offset, byte[] payload)
    {
        BinaryPrimitives.WriteUInt32BigEndian(dest.AsSpan(offset, 4), (uint)payload.Length);
        offset += 4;
        Buffer.BlockCopy(payload, 0, dest, offset, payload.Length);
        offset += payload.Length;
    }
}
