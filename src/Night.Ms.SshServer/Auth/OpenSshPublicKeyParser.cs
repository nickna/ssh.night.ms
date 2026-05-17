using System.Buffers.Binary;
using System.Security.Cryptography;

namespace Night.Ms.SshServer.Auth;

// Parses an OpenSSH-format public key — the single-line form found in
// ~/.ssh/id_ed25519.pub. Format: "<type> <base64-blob> [comment]".
// Validates that the embedded type-string (RFC 4253 § 6.6 wire format) matches the
// algorithm token, then computes SHA256:base64 fingerprint matching what BbsSshServer
// derives via ComputeFingerprint.
public static class OpenSshPublicKeyParser
{
    public static bool TryParse(string text, out ParsedKey parsed)
    {
        parsed = default!;
        if (string.IsNullOrWhiteSpace(text)) return false;

        var trimmed = text.Trim();
        var parts = trimmed.Split([' ', '\t'], 3, StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length < 2) return false;

        var algorithm = parts[0];
        if (!IsKnownAlgorithm(algorithm)) return false;

        byte[] blob;
        try { blob = Convert.FromBase64String(parts[1]); }
        catch { return false; }
        if (blob.Length == 0) return false;

        // First field of the blob is a length-prefixed string holding the key type. If it
        // doesn't match the declared algorithm, the user pasted a mangled key.
        if (!TryReadLengthPrefixedString(blob, out var embeddedType) || embeddedType != algorithm)
        {
            return false;
        }

        var hash = SHA256.HashData(blob);
        var fingerprint = "SHA256:" + Convert.ToBase64String(hash).TrimEnd('=');
        var comment = parts.Length > 2 ? parts[2] : null;
        parsed = new ParsedKey(algorithm, blob, fingerprint, comment);
        return true;
    }

    private static bool IsKnownAlgorithm(string algo) => algo switch
    {
        "ssh-ed25519" or "ssh-rsa" or "ecdsa-sha2-nistp256" or "ecdsa-sha2-nistp384" or "ecdsa-sha2-nistp521" => true,
        _ => false,
    };

    private static bool TryReadLengthPrefixedString(byte[] blob, out string value)
    {
        value = string.Empty;
        if (blob.Length < 4) return false;
        var length = BinaryPrimitives.ReadUInt32BigEndian(blob.AsSpan(0, 4));
        if (length > int.MaxValue || 4 + length > (uint)blob.Length) return false;
        value = System.Text.Encoding.ASCII.GetString(blob, 4, (int)length);
        return true;
    }

    public sealed record ParsedKey(string Algorithm, byte[] Blob, string Fingerprint, string? Comment);
}
