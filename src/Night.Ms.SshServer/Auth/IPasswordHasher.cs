namespace Night.Ms.SshServer.Auth;

// Two-piece password representation: the raw hash bytes (User.PasswordHash) and an algo
// descriptor string (User.PasswordAlgo) that carries the exact parameters used to produce
// the hash. Keeping the parameters with the hash lets us bump cost factors over time without
// invalidating existing users — on next successful login, NeedsRehash flags drift and the
// caller can re-hash with the new defaults.
public interface IPasswordHasher
{
    // Returns (hashBytes, algoString) — algoString shape is "argon2id:m=65536,t=3,p=1,s=16,h=32".
    HashedPassword Hash(string password);

    // Constant-time verify against a stored hash. Always runs an Argon2id hash internally —
    // callers that need timing-equivalence for "user doesn't exist" or "user has no password"
    // should call VerifyDummy() instead of skipping the verify; both paths take roughly the
    // same wall time so timing analysis can't distinguish them.
    bool Verify(string password, byte[] storedHash, string? storedAlgo);

    // For unknown handles / no-password users. Performs the same work as Verify against a
    // precomputed throwaway hash so the auth path is timing-equivalent regardless of user
    // existence. Always returns false.
    void VerifyDummy(string password);

    // True if the stored algo string is missing or its parameters differ from the current
    // configured defaults. Caller can re-hash on the next successful login.
    bool NeedsRehash(string? storedAlgo);
}

public readonly record struct HashedPassword(byte[] Hash, string Algo);
