using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshTransport;

namespace Night.Ms.SshServer.Tests;

public class AuthLookupServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private AuthLookupService? _sut;
    private NoopPasswordHasher? _hasher;
    private NoopRateLimiter? _rateLimiter;

    public AuthLookupServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        _hasher = new NoopPasswordHasher();
        _rateLimiter = new NoopRateLimiter();
        _sut = new AuthLookupService(new TestDbContextFactory(_dbOptions), _hasher, _rateLimiter, NullLogger<AuthLookupService>.Instance);
    }

    public Task DisposeAsync() => Task.CompletedTask;

    private static AuthQuery.PublicKey Pubkey(string handle, string fingerprint, string algorithm = "ssh-ed25519") =>
        new(handle, fingerprint, algorithm, [0xDE, 0xAD, 0xBE, 0xEF], SourceIp: null);

    private async Task<long> SeedUserAsync(string handle, string? fingerprint = null, bool isSysop = false, bool isBanned = false, byte[]? passwordHash = null, string? passwordAlgo = null, bool requireSshKey = false)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User
        {
            Handle = handle,
            CreatedAt = DateTimeOffset.UtcNow,
            IsSysop = isSysop,
            IsBanned = isBanned,
            PasswordHash = passwordHash,
            PasswordAlgo = passwordAlgo,
            RequireSshKey = requireSshKey,
        };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        if (fingerprint is not null)
        {
            db.IdentityCredentials.Add(new IdentityCredential
            {
                UserId = user.Id,
                Provider = CredentialProvider.Ssh,
                Subject = fingerprint,
                CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
        }
        return user.Id;
    }

    [Fact]
    public async Task Unknown_handle_returns_SignupRequired_with_handle_and_offered_key()
    {
        var decision = await _sut!.LookupAsync(Pubkey("nobody", "SHA256:offered"), default);
        var signup = Assert.IsType<AuthDecision.SignupRequired>(decision);
        Assert.Equal("nobody", signup.Handle);
        Assert.Equal("SHA256:offered", signup.OfferedFingerprint);
    }

    [Fact]
    public async Task Known_handle_with_matching_key_returns_Known()
    {
        var userId = await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey");

        var decision = await _sut!.LookupAsync(Pubkey("alice", "SHA256:alicekey"), default);

        var known = Assert.IsType<AuthDecision.Known>(decision);
        Assert.Equal(userId, known.UserId);
        Assert.Equal("alice", known.Handle);
        Assert.False(known.IsSysop);
    }

    [Fact]
    public async Task Sysop_flag_is_propagated_into_decision()
    {
        await SeedUserAsync(handle: "nick", fingerprint: "SHA256:nickkey", isSysop: true);

        var decision = await _sut!.LookupAsync(Pubkey("nick", "SHA256:nickkey"), default);

        var known = Assert.IsType<AuthDecision.Known>(decision);
        Assert.True(known.IsSysop);
    }

    [Fact]
    public async Task Banned_user_returns_Banned()
    {
        await SeedUserAsync(handle: "troll", fingerprint: "SHA256:trollkey", isBanned: true);

        var decision = await _sut!.LookupAsync(Pubkey("troll", "SHA256:trollkey"), default);

        Assert.IsType<AuthDecision.Banned>(decision);
    }

    [Fact]
    public async Task Banned_takes_precedence_over_sysop()
    {
        await SeedUserAsync(handle: "ex_sysop", fingerprint: "SHA256:exsysopkey", isSysop: true, isBanned: true);

        var decision = await _sut!.LookupAsync(Pubkey("ex_sysop", "SHA256:exsysopkey"), default);

        Assert.IsType<AuthDecision.Banned>(decision);
    }

    [Fact]
    public async Task Known_handle_with_unregistered_key_returns_Refused()
    {
        // Handle exists, but the key offered isn't on file → client should fall back to
        // password. Server signals this via Refused; transport translates to null and the
        // SSH layer advertises Password as the next method.
        await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey");

        var decision = await _sut!.LookupAsync(Pubkey("alice", "SHA256:OTHER"), default);

        Assert.IsType<AuthDecision.Refused>(decision);
    }

    [Fact]
    public async Task Key_attached_to_other_user_does_NOT_grant_access_to_named_handle()
    {
        // Subject is globally unique by index (Provider, Subject). The lookup must also
        // confirm that the credential is owned by the named handle — otherwise sharing a
        // fingerprint would let me log in as anyone whose key is on file.
        await SeedUserAsync(handle: "alice", fingerprint: "SHA256:keyA");
        await SeedUserAsync(handle: "bob");

        var decision = await _sut!.LookupAsync(Pubkey("bob", "SHA256:keyA"), default);

        Assert.IsType<AuthDecision.Refused>(decision);
    }

    [Fact]
    public async Task Multiple_keys_per_user_each_resolve_to_the_same_account()
    {
        var userId = await SeedUserAsync(handle: "multi", fingerprint: "SHA256:laptop");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.IdentityCredentials.Add(new IdentityCredential
            {
                UserId = userId,
                Provider = CredentialProvider.Ssh,
                Subject = "SHA256:desktop",
                CreatedAt = DateTimeOffset.UtcNow,
            });
            await db.SaveChangesAsync();
        }

        var laptop = await _sut!.LookupAsync(Pubkey("multi", "SHA256:laptop"), default);
        var desktop = await _sut!.LookupAsync(Pubkey("multi", "SHA256:desktop"), default);

        Assert.Equal(userId, ((AuthDecision.Known)laptop).UserId);
        Assert.Equal(userId, ((AuthDecision.Known)desktop).UserId);
    }

    [Fact]
    public async Task Handle_uniqueness_is_case_insensitive_via_citext()
    {
        await SeedUserAsync(handle: "Alice", fingerprint: "SHA256:k1");

        await using var db = new AppDbContext(_dbOptions!);
        db.Users.Add(new User
        {
            Handle = "alice",
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await Assert.ThrowsAsync<DbUpdateException>(() => db.SaveChangesAsync());
    }

    [Fact]
    public async Task Password_auth_with_correct_password_returns_Known()
    {
        var hashed = _hasher!.Hash("correct horse battery staple");
        await SeedUserAsync(handle: "alice", passwordHash: hashed.Hash, passwordAlgo: hashed.Algo);

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "correct horse battery staple", SourceIp: null), default);

        Assert.IsType<AuthDecision.Known>(decision);
    }

    [Fact]
    public async Task Password_auth_with_wrong_password_returns_Refused_and_records_failure()
    {
        var hashed = _hasher!.Hash("right");
        await SeedUserAsync(handle: "alice", passwordHash: hashed.Hash, passwordAlgo: hashed.Algo);

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "wrong", SourceIp: null), default);

        Assert.IsType<AuthDecision.Refused>(decision);
        Assert.Equal(1, _rateLimiter!.FailureCount);
    }

    [Fact]
    public async Task Password_auth_for_unknown_handle_burns_dummy_hash_and_returns_SignupRequired()
    {
        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("nobody", "anything", SourceIp: null), default);

        Assert.IsType<AuthDecision.SignupRequired>(decision);
        Assert.True(_hasher!.DummyVerifyCount >= 1, "must burn a dummy hash to mask user existence timing");
    }

    [Fact]
    public async Task Password_auth_for_user_without_password_returns_Refused_and_burns_dummy_hash()
    {
        await SeedUserAsync(handle: "alice"); // no password set

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "anything", SourceIp: null), default);

        Assert.IsType<AuthDecision.Refused>(decision);
        Assert.True(_hasher!.DummyVerifyCount >= 1);
    }

    [Fact]
    public async Task Locked_out_handle_returns_RateLimited_without_verifying()
    {
        var hashed = _hasher!.Hash("correct");
        await SeedUserAsync(handle: "alice", passwordHash: hashed.Hash, passwordAlgo: hashed.Algo);
        _rateLimiter!.LockOut("alice", TimeSpan.FromMinutes(15));

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "correct", SourceIp: null), default);

        // Locked-out attempts still take wall time via VerifyDummy, but the real verify
        // never runs. We don't want correct-password-while-locked-out to succeed.
        Assert.IsType<AuthDecision.RateLimited>(decision);
    }

    [Fact]
    public async Task RequireSshKey_refuses_password_login_even_with_correct_password()
    {
        // Passwordless mode: a known handle with a verifiable password and the toggle ON
        // must be Refused — the right password does not let you in. Burning the full
        // verify ensures the timing matches a successful non-passwordless login, so the
        // toggle state isn't leakable via wall-clock side-channel.
        var hashed = _hasher!.Hash("correct horse battery staple");
        await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey", passwordHash: hashed.Hash, passwordAlgo: hashed.Algo, requireSshKey: true);

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "correct horse battery staple", SourceIp: null), default);

        Assert.IsType<AuthDecision.Refused>(decision);
        Assert.Equal(1, _rateLimiter!.FailureCount);
    }

    [Fact]
    public async Task RequireSshKey_with_wrong_password_still_returns_Refused()
    {
        // Wrong password takes its normal Refused path — we don't want the toggle to mask
        // a wrong-password attempt as something different, since both outcomes are the same
        // failure shape from the client's view.
        var hashed = _hasher!.Hash("right");
        await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey", passwordHash: hashed.Hash, passwordAlgo: hashed.Algo, requireSshKey: true);

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "wrong", SourceIp: null), default);

        Assert.IsType<AuthDecision.Refused>(decision);
    }

    [Fact]
    public async Task RequireSshKey_allows_matching_registered_key()
    {
        // The whole point of passwordless mode: a registered key still authenticates.
        // No change from non-passwordless on the key path.
        var userId = await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey", requireSshKey: true);

        var decision = await _sut!.LookupAsync(Pubkey("alice", "SHA256:alicekey"), default);

        var known = Assert.IsType<AuthDecision.Known>(decision);
        Assert.Equal(userId, known.UserId);
    }

    [Fact]
    public async Task RequireSshKey_still_refuses_key_not_in_users_keyring()
    {
        // Strict per-user already refuses cross-user key login; the passwordless toggle
        // shouldn't change that path either. This test exists to pin the behavior so a
        // future refactor of EvaluateKeyAsync doesn't accidentally relax it.
        await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey", requireSshKey: true);
        await SeedUserAsync(handle: "bob", fingerprint: "SHA256:bobkey");

        var decision = await _sut!.LookupAsync(Pubkey("alice", "SHA256:bobkey"), default);

        Assert.IsType<AuthDecision.Refused>(decision);
    }

    [Fact]
    public async Task Successful_password_login_clears_rate_limiter_for_that_handle()
    {
        var hashed = _hasher!.Hash("correct");
        await SeedUserAsync(handle: "alice", passwordHash: hashed.Hash, passwordAlgo: hashed.Algo);
        _rateLimiter!.RecordedFailures.Add("alice");

        var decision = await _sut!.LookupAsync(
            new AuthQuery.Password("alice", "correct", SourceIp: null), default);

        Assert.IsType<AuthDecision.Known>(decision);
        Assert.Contains("alice", _rateLimiter!.Cleared);
    }
}

// In-test password hasher. NoopPasswordHasher returns predictable bytes so the verify path is
// deterministic without paying the Argon2id cost on every test. Hash is just SHA256(password)
// so collisions don't matter for these tests.
internal sealed class NoopPasswordHasher : IPasswordHasher
{
    public int DummyVerifyCount;

    public HashedPassword Hash(string password)
    {
        var hash = System.Security.Cryptography.SHA256.HashData(System.Text.Encoding.UTF8.GetBytes(password));
        return new HashedPassword(hash, "noop:sha256");
    }

    public bool Verify(string password, byte[] storedHash, string? storedAlgo)
    {
        var actual = System.Security.Cryptography.SHA256.HashData(System.Text.Encoding.UTF8.GetBytes(password));
        return System.Security.Cryptography.CryptographicOperations.FixedTimeEquals(actual, storedHash);
    }

    public void VerifyDummy(string password)
    {
        Interlocked.Increment(ref DummyVerifyCount);
    }

    public bool NeedsRehash(string? storedAlgo) => false;
}

internal sealed class NoopRateLimiter : ILoginRateLimiter
{
    public int FailureCount;
    public readonly List<string> RecordedFailures = new();
    public readonly HashSet<string> Cleared = new();
    private readonly Dictionary<string, DateTimeOffset> _lockouts = new();

    public Task<RateLimitCheck> CheckAsync(string handle, System.Net.IPAddress? sourceIp, CancellationToken ct)
    {
        if (_lockouts.TryGetValue(handle, out var until) && until > DateTimeOffset.UtcNow)
        {
            return Task.FromResult<RateLimitCheck>(new RateLimitCheck.LockedOut(until - DateTimeOffset.UtcNow, "handle"));
        }
        return Task.FromResult<RateLimitCheck>(RateLimitCheck.Allowed.Instance);
    }

    public Task RecordFailureAsync(string handle, System.Net.IPAddress? sourceIp, CancellationToken ct)
    {
        Interlocked.Increment(ref FailureCount);
        RecordedFailures.Add(handle);
        return Task.CompletedTask;
    }

    public Task ClearAsync(string handle, CancellationToken ct)
    {
        Cleared.Add(handle);
        _lockouts.Remove(handle);
        return Task.CompletedTask;
    }

    public void LockOut(string handle, TimeSpan duration) => _lockouts[handle] = DateTimeOffset.UtcNow + duration;
}
