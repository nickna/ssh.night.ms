using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
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

    public AuthLookupServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        _sut = new AuthLookupService(new TestDbContextFactory(_dbOptions), NullLogger<AuthLookupService>.Instance);
    }

    public Task DisposeAsync() => Task.CompletedTask;

    private static AuthQuery Query(string fingerprint, string handle = "guest", string algorithm = "ssh-ed25519") =>
        new(fingerprint, algorithm, PublicKeyBlob: [0xDE, 0xAD, 0xBE, 0xEF], Username: handle);

    private async Task<long> SeedUserAsync(string handle, string fingerprint, bool isSysop = false, bool isBanned = false)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User
        {
            Handle = handle,
            CreatedAt = DateTimeOffset.UtcNow,
            IsSysop = isSysop,
            IsBanned = isBanned,
        };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        db.IdentityCredentials.Add(new IdentityCredential
        {
            UserId = user.Id,
            Provider = CredentialProvider.Ssh,
            Subject = fingerprint,
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await db.SaveChangesAsync();
        return user.Id;
    }

    [Fact]
    public async Task Empty_database_returns_Unknown_for_any_fingerprint()
    {
        var decision = await _sut!.LookupAsync(Query("SHA256:does-not-exist"), default);
        Assert.IsType<AuthDecision.Unknown>(decision);
    }

    [Fact]
    public async Task Known_user_returns_Known_with_handle_and_id_and_sysop_flag()
    {
        var userId = await SeedUserAsync(handle: "alice", fingerprint: "SHA256:alicekey", isSysop: false);

        var decision = await _sut!.LookupAsync(Query("SHA256:alicekey"), default);

        var known = Assert.IsType<AuthDecision.Known>(decision);
        Assert.Equal(userId, known.UserId);
        Assert.Equal("alice", known.Handle);
        Assert.False(known.IsSysop);
    }

    [Fact]
    public async Task Sysop_flag_is_propagated_into_decision()
    {
        await SeedUserAsync(handle: "nick", fingerprint: "SHA256:nickkey", isSysop: true);

        var decision = await _sut!.LookupAsync(Query("SHA256:nickkey"), default);

        var known = Assert.IsType<AuthDecision.Known>(decision);
        Assert.True(known.IsSysop);
    }

    [Fact]
    public async Task Banned_user_returns_Banned_not_Known()
    {
        await SeedUserAsync(handle: "troll", fingerprint: "SHA256:trollkey", isBanned: true);

        var decision = await _sut!.LookupAsync(Query("SHA256:trollkey"), default);

        Assert.IsType<AuthDecision.Banned>(decision);
    }

    [Fact]
    public async Task Banned_takes_precedence_over_sysop()
    {
        // Defensive: if a sysop somehow gets banned (operator error), they're banned, not sysop.
        await SeedUserAsync(handle: "ex_sysop", fingerprint: "SHA256:exsysopkey", isSysop: true, isBanned: true);

        var decision = await _sut!.LookupAsync(Query("SHA256:exsysopkey"), default);

        Assert.IsType<AuthDecision.Banned>(decision);
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

        var laptop = await _sut!.LookupAsync(Query("SHA256:laptop"), default);
        var desktop = await _sut!.LookupAsync(Query("SHA256:desktop"), default);

        Assert.Equal(userId, ((AuthDecision.Known)laptop).UserId);
        Assert.Equal(userId, ((AuthDecision.Known)desktop).UserId);
        Assert.Equal("multi", ((AuthDecision.Known)laptop).Handle);
        Assert.Equal("multi", ((AuthDecision.Known)desktop).Handle);
    }

    [Fact]
    public async Task Fingerprint_lookup_is_case_sensitive()
    {
        // IdentityCredential.Subject is plain text (not citext) — fingerprints embed base64,
        // which is case-sensitive on purpose; flipping case would change the underlying key.
        await SeedUserAsync(handle: "exact", fingerprint: "SHA256:abc123XYZ");

        var lower = await _sut!.LookupAsync(Query("sha256:abc123xyz"), default);
        var exact = await _sut!.LookupAsync(Query("SHA256:abc123XYZ"), default);

        Assert.IsType<AuthDecision.Unknown>(lower);
        Assert.IsType<AuthDecision.Known>(exact);
    }

    [Fact]
    public async Task Handle_uniqueness_is_case_insensitive_via_citext()
    {
        await SeedUserAsync(handle: "Alice", fingerprint: "SHA256:k1");

        // Inserting another user with the same handle in different case must violate the
        // unique index thanks to citext on the handle column.
        await using var db = new AppDbContext(_dbOptions!);
        db.Users.Add(new User
        {
            Handle = "alice",
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await Assert.ThrowsAsync<DbUpdateException>(() => db.SaveChangesAsync());
    }

    [Fact]
    public async Task Cancellation_token_propagates()
    {
        await SeedUserAsync(handle: "anyone", fingerprint: "SHA256:anykey");

        using var cts = new CancellationTokenSource();
        cts.Cancel();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(
            () => _sut!.LookupAsync(Query("SHA256:anykey"), cts.Token));
    }
}
