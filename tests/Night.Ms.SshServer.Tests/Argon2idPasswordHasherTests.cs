using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Configuration;

namespace Night.Ms.SshServer.Tests;

public class Argon2idPasswordHasherTests
{
    // Use tiny parameters so the test suite stays fast — production values come from
    // NightMsOptions.PasswordHashing which defaults to OWASP 2024.
    private static NightMsOptions TinyOptions() => new()
    {
        PasswordHashing = new PasswordHashingOptions
        {
            MemoryKb = 4096, // 4 MiB
            Iterations = 1,
            Parallelism = 1,
            SaltBytes = 16,
            HashBytes = 32,
            MinPasswordLength = 10,
        },
    };

    [Fact]
    public void Hash_then_Verify_succeeds_for_same_password()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        var hashed = hasher.Hash("correct horse battery staple");

        Assert.True(hasher.Verify("correct horse battery staple", hashed.Hash, hashed.Algo));
    }

    [Fact]
    public void Verify_fails_for_wrong_password()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        var hashed = hasher.Hash("right");

        Assert.False(hasher.Verify("wrong", hashed.Hash, hashed.Algo));
    }

    [Fact]
    public void Algo_string_encodes_parameters()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        var hashed = hasher.Hash("anything");

        Assert.Equal("argon2id:m=4096,t=1,p=1,s=16,h=32", hashed.Algo);
    }

    [Fact]
    public void Different_salts_produce_different_hashes_for_same_password()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        var a = hasher.Hash("same password");
        var b = hasher.Hash("same password");

        // Same password, different random salts → different hash bytes. But verify still
        // succeeds for both against their own stored value.
        Assert.NotEqual(a.Hash, b.Hash);
        Assert.True(hasher.Verify("same password", a.Hash, a.Algo));
        Assert.True(hasher.Verify("same password", b.Hash, b.Algo));
    }

    [Fact]
    public void NeedsRehash_true_when_stored_params_differ_from_current()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        // Stored hash was made with different iteration count
        Assert.True(hasher.NeedsRehash("argon2id:m=4096,t=99,p=1,s=16,h=32"));
        Assert.True(hasher.NeedsRehash(null));
        Assert.True(hasher.NeedsRehash(""));
        Assert.True(hasher.NeedsRehash("bcrypt:..."));
    }

    [Fact]
    public void NeedsRehash_false_when_stored_params_match()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        var hashed = hasher.Hash("x");
        Assert.False(hasher.NeedsRehash(hashed.Algo));
    }

    [Fact]
    public void VerifyDummy_returns_without_throwing_for_any_input()
    {
        var hasher = new Argon2idPasswordHasher(TinyOptions());
        // The point of VerifyDummy is timing-equivalence — exercise it with a few shapes
        // to make sure it doesn't crash on edge inputs.
        hasher.VerifyDummy("");
        hasher.VerifyDummy("normal");
        hasher.VerifyDummy(new string('a', 10000));
    }
}
