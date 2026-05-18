namespace Night.Ms.SshServer.Doors;

// Abstraction over the source of randomness for door games. The production implementation
// is CryptoGameRng (System.Security.Cryptography); tests substitute a deterministic fake.
// A seedable System.Random would be too easy to predict — slot/poker outcomes need to be
// non-reproducible across rounds even if an attacker knows the time and prior history.
public interface IGameRng
{
    int Next(int maxExclusive);
    int Next(int minInclusive, int maxExclusive);
    double NextDouble();
}
