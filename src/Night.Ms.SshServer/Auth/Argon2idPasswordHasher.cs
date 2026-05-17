using System.Security.Cryptography;
using System.Text;
using Konscious.Security.Cryptography;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Auth;

// Konscious-backed Argon2id. The hash bytes stored on User.PasswordHash are SALT || HASH —
// salt length comes from the algo string ("s=16" → first 16 bytes are salt). This avoids
// adding a separate salt column. The algo string is human-readable on purpose so a sysop
// can eyeball stored values during incident response.
public sealed class Argon2idPasswordHasher(NightMsOptions options) : IPasswordHasher
{
    private readonly PasswordHashingOptions _opts = options.PasswordHashing;

    // Precomputed throwaway hash with the same parameters so VerifyDummy work matches Verify.
    // Generated lazily on first use — that initial hash is the only one not amortized.
    private byte[]? _dummyHash;
    private string? _dummyAlgo;
    private readonly Lock _dummyInit = new();

    public HashedPassword Hash(string password)
    {
        var salt = RandomNumberGenerator.GetBytes(_opts.SaltBytes);
        var hash = ComputeHash(password, salt, _opts.MemoryKb, _opts.Iterations, _opts.Parallelism, _opts.HashBytes);
        var combined = new byte[salt.Length + hash.Length];
        Buffer.BlockCopy(salt, 0, combined, 0, salt.Length);
        Buffer.BlockCopy(hash, 0, combined, salt.Length, hash.Length);
        return new HashedPassword(combined, FormatAlgo(_opts.MemoryKb, _opts.Iterations, _opts.Parallelism, _opts.SaltBytes, _opts.HashBytes));
    }

    public bool Verify(string password, byte[] storedHash, string? storedAlgo)
    {
        if (!TryParseAlgo(storedAlgo, out var p))
        {
            // Stored algo missing or malformed: fall back to current params. If verify fails
            // here, it's a real mismatch; if it succeeds, NeedsRehash will fire and the caller
            // rehashes with the proper algo string.
            p = (_opts.MemoryKb, _opts.Iterations, _opts.Parallelism, _opts.SaltBytes, _opts.HashBytes);
        }
        if (storedHash.Length != p.SaltBytes + p.HashBytes) return false;

        var salt = storedHash[..p.SaltBytes];
        var expected = storedHash[p.SaltBytes..];
        var actual = ComputeHash(password, salt, p.MemoryKb, p.Iterations, p.Parallelism, p.HashBytes);
        return CryptographicOperations.FixedTimeEquals(expected, actual);
    }

    public void VerifyDummy(string password)
    {
        EnsureDummy();
        // Discard result — point is the wall-clock work, not correctness.
        _ = Verify(password, _dummyHash!, _dummyAlgo);
    }

    public bool NeedsRehash(string? storedAlgo)
    {
        if (!TryParseAlgo(storedAlgo, out var p)) return true;
        return p.MemoryKb != _opts.MemoryKb
            || p.Iterations != _opts.Iterations
            || p.Parallelism != _opts.Parallelism
            || p.SaltBytes != _opts.SaltBytes
            || p.HashBytes != _opts.HashBytes;
    }

    private static byte[] ComputeHash(string password, byte[] salt, int memoryKb, int iterations, int parallelism, int hashBytes)
    {
        // Konscious refuses zero-length passwords. An empty string can legitimately reach
        // this method via VerifyDummy on an attacker probe — substitute a single null byte
        // so the timing work runs but no real password ever produces a matching hash
        // (production passwords have a minimum length enforced upstream).
        var pwBytes = password.Length == 0 ? new byte[] { 0 } : Encoding.UTF8.GetBytes(password);
        using var argon = new Argon2id(pwBytes)
        {
            Salt = salt,
            DegreeOfParallelism = parallelism,
            MemorySize = memoryKb,
            Iterations = iterations,
        };
        return argon.GetBytes(hashBytes);
    }

    private static string FormatAlgo(int memoryKb, int iterations, int parallelism, int saltBytes, int hashBytes) =>
        $"argon2id:m={memoryKb},t={iterations},p={parallelism},s={saltBytes},h={hashBytes}";

    private static bool TryParseAlgo(string? algo, out (int MemoryKb, int Iterations, int Parallelism, int SaltBytes, int HashBytes) parsed)
    {
        parsed = default;
        if (string.IsNullOrEmpty(algo) || !algo.StartsWith("argon2id:", StringComparison.Ordinal)) return false;
        var body = algo[(algo.IndexOf(':') + 1)..];
        int m = 0, t = 0, p = 0, s = 0, h = 0;
        foreach (var part in body.Split(','))
        {
            var eq = part.IndexOf('=');
            if (eq < 0) return false;
            var key = part[..eq];
            if (!int.TryParse(part[(eq + 1)..], out var value)) return false;
            switch (key)
            {
                case "m": m = value; break;
                case "t": t = value; break;
                case "p": p = value; break;
                case "s": s = value; break;
                case "h": h = value; break;
                default: return false;
            }
        }
        if (m == 0 || t == 0 || p == 0 || s == 0 || h == 0) return false;
        parsed = (m, t, p, s, h);
        return true;
    }

    private void EnsureDummy()
    {
        if (_dummyHash is not null) return;
        lock (_dummyInit)
        {
            if (_dummyHash is not null) return;
            // Use a fixed password literal so the dummy hash is deterministic per
            // server-config (same params → same hash). Random would defeat the timing
            // equivalence by varying CPU branch behavior.
            var seed = Hash("nightms-dummy-hash-seed-1234567890");
            _dummyHash = seed.Hash;
            _dummyAlgo = seed.Algo;
        }
    }
}
