using System.Security.Cryptography;

namespace Night.Ms.SshServer.Doors;

public sealed class CryptoGameRng : IGameRng
{
    public int Next(int maxExclusive) => RandomNumberGenerator.GetInt32(maxExclusive);

    public int Next(int minInclusive, int maxExclusive) =>
        RandomNumberGenerator.GetInt32(minInclusive, maxExclusive);

    public double NextDouble()
    {
        // 53 random bits → double in [0, 1) without bias from float rounding.
        Span<byte> buf = stackalloc byte[8];
        RandomNumberGenerator.Fill(buf);
        var bits = BitConverter.ToUInt64(buf) >> 11;
        return bits / (double)(1UL << 53);
    }
}
