using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Auth;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;

namespace Night.Ms.SshServer.Tests;

public class IdentityResolutionServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;

    public IdentityResolutionServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync() => _dbOptions = await _fixture.CreateFreshDatabaseAsync();

    public Task DisposeAsync() => Task.CompletedTask;

    private AppDbContext NewDb() => new(_dbOptions!);
    private IdentityResolutionService NewSut(AppDbContext db) => new(db);
    private static bool NeverBootstrap(string handle) => false;

    private async Task<long> SeedUserAsync(string handle, string? email = null, bool isBanned = false)
    {
        await using var db = NewDb();
        var user = new User
        {
            Handle = handle,
            Email = email,
            CreatedAt = DateTimeOffset.UtcNow,
            IsBanned = isBanned,
        };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user.Id;
    }

    private async Task SeedCredentialAsync(long userId, CredentialProvider provider, string subject)
    {
        await using var db = NewDb();
        db.IdentityCredentials.Add(new IdentityCredential
        {
            UserId = userId,
            Provider = provider,
            Subject = subject,
            CreatedAt = DateTimeOffset.UtcNow,
        });
        await db.SaveChangesAsync();
    }

    [Fact]
    public async Task Resolve_known_subject_returns_Existing_and_bumps_last_used()
    {
        var userId = await SeedUserAsync("alice", "alice@example.com");
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|alice-sub");

        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.ResolveAsync(
            CredentialProvider.Google, "google|alice-sub",
            email: "alice@example.com", emailVerified: true, extraMetadata: null,
            cancellationToken: default);

        var existing = Assert.IsType<IdentityResolution.Existing>(result);
        Assert.Equal(userId, existing.UserId);
        Assert.Equal("alice", existing.Handle);

        var bumped = await db.IdentityCredentials.SingleAsync();
        Assert.NotNull(bumped.LastUsedAt);
    }

    [Fact]
    public async Task Resolve_unknown_subject_with_verified_email_auto_links_to_existing_user()
    {
        var userId = await SeedUserAsync("alice", "alice@example.com");
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|alice-sub");

        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.ResolveAsync(
            CredentialProvider.Microsoft, "ms|alice-sub",
            email: "alice@example.com", emailVerified: true, extraMetadata: null,
            cancellationToken: default);

        var linked = Assert.IsType<IdentityResolution.LinkedToExisting>(result);
        Assert.Equal(userId, linked.UserId);

        // Both credentials now point at the same user.
        var creds = await db.IdentityCredentials.Where(c => c.UserId == userId).ToListAsync();
        Assert.Equal(2, creds.Count);

        // Audit row written with by='auto-link'.
        var audit = await db.AuditLogs.SingleAsync(a => a.Action == "identity.linked");
        Assert.Equal(userId, audit.ActorId);
    }

    [Fact]
    public async Task Resolve_unknown_subject_with_unverified_email_never_auto_links()
    {
        var userId = await SeedUserAsync("alice", "alice@example.com");
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|alice-sub");

        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.ResolveAsync(
            CredentialProvider.Microsoft, "ms|alice-sub",
            email: "alice@example.com", emailVerified: false, extraMetadata: null,
            cancellationToken: default);

        Assert.IsType<IdentityResolution.NewSignup>(result);

        // Only the seeded credential exists; the unverified attempt did not link.
        var count = await db.IdentityCredentials.CountAsync();
        Assert.Equal(1, count);
    }

    [Fact]
    public async Task Resolve_unknown_subject_with_no_email_match_returns_NewSignup_with_suggested_handle()
    {
        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.ResolveAsync(
            CredentialProvider.Google, "google|newcomer-sub",
            email: "Bob.Smith@example.com", emailVerified: true, extraMetadata: null,
            cancellationToken: default);

        var signup = Assert.IsType<IdentityResolution.NewSignup>(result);
        Assert.Equal("BobSmith", signup.SuggestedHandle);
        Assert.True(signup.EmailVerified);
    }

    [Fact]
    public async Task Resolve_banned_existing_user_returns_Banned_outcome()
    {
        var userId = await SeedUserAsync("troll", "troll@example.com", isBanned: true);
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|troll-sub");

        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.ResolveAsync(
            CredentialProvider.Google, "google|troll-sub",
            email: "troll@example.com", emailVerified: true, extraMetadata: null,
            cancellationToken: default);

        Assert.IsType<IdentityResolution.Banned>(result);
    }

    [Fact]
    public async Task CreateUser_writes_user_credential_and_signup_audit_row()
    {
        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.CreateUserAndCredentialAsync(
            handle: "carol",
            provider: CredentialProvider.Google,
            subject: "google|carol-sub",
            email: "carol@example.com",
            emailVerified: true,
            extraMetadata: null,
            isBootstrapHandle: NeverBootstrap,
            cancellationToken: default);

        Assert.NotNull(result);
        var (_, handle, isSysop) = result!.Value;
        Assert.Equal("carol", handle);
        Assert.False(isSysop);

        var user = await db.Users.SingleAsync();
        Assert.Equal("carol@example.com", user.Email);
        var credential = await db.IdentityCredentials.SingleAsync();
        Assert.Equal(CredentialProvider.Google, credential.Provider);
        var audit = await db.AuditLogs.SingleAsync(a => a.Action == "identity.signup");
        Assert.Null(audit.ActorId);
    }

    [Fact]
    public async Task CreateUser_with_existing_handle_returns_null()
    {
        await SeedUserAsync("dupe", "first@example.com");

        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.CreateUserAndCredentialAsync(
            handle: "dupe",
            provider: CredentialProvider.Google,
            subject: "google|dupe-sub",
            email: "second@example.com",
            emailVerified: true,
            extraMetadata: null,
            isBootstrapHandle: NeverBootstrap,
            cancellationToken: default);

        Assert.Null(result);
    }

    [Fact]
    public async Task CreateUser_with_bootstrap_handle_promotes_to_sysop_and_audits()
    {
        await using var db = NewDb();
        var sut = NewSut(db);
        var result = await sut.CreateUserAndCredentialAsync(
            handle: "nick",
            provider: CredentialProvider.Google,
            subject: "google|nick-sub",
            email: "nick@example.com",
            emailVerified: true,
            extraMetadata: null,
            isBootstrapHandle: h => h == "nick",
            cancellationToken: default);

        Assert.NotNull(result);
        Assert.True(result!.Value.IsSysop);
        Assert.NotNull(await db.AuditLogs.SingleOrDefaultAsync(a => a.Action == "sysop.bootstrap"));
    }

    [Fact]
    public async Task LinkToUser_adds_credential_and_audits_manual_link()
    {
        var userId = await SeedUserAsync("dave");
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|dave-sub");

        await using var db = NewDb();
        var sut = NewSut(db);
        var outcome = await sut.LinkToUserAsync(
            userId, CredentialProvider.Microsoft, "ms|dave-sub",
            email: "dave@example.com", emailVerified: true, extraMetadata: null,
            cancellationToken: default);

        Assert.IsType<LinkOutcome.Linked>(outcome);
        var count = await db.IdentityCredentials.CountAsync(c => c.UserId == userId);
        Assert.Equal(2, count);

        // Email was backfilled because Dave had none and this credential supplied a verified one.
        var user = await db.Users.SingleAsync(u => u.Id == userId);
        Assert.Equal("dave@example.com", user.Email);
    }

    [Fact]
    public async Task LinkToUser_when_subject_already_owned_by_someone_else_refuses()
    {
        var aliceId = await SeedUserAsync("alice");
        await SeedCredentialAsync(aliceId, CredentialProvider.Google, "google|shared-sub");
        var bobId = await SeedUserAsync("bob");

        await using var db = NewDb();
        var sut = NewSut(db);
        var outcome = await sut.LinkToUserAsync(
            bobId, CredentialProvider.Google, "google|shared-sub",
            email: null, emailVerified: false, extraMetadata: null,
            cancellationToken: default);

        Assert.IsType<LinkOutcome.AlreadyLinkedToOther>(outcome);
    }

    [Fact]
    public async Task LinkToUser_when_subject_already_owned_by_caller_reports_AlreadyLinkedToYou()
    {
        var userId = await SeedUserAsync("eve");
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|eve-sub");

        await using var db = NewDb();
        var sut = NewSut(db);
        var outcome = await sut.LinkToUserAsync(
            userId, CredentialProvider.Google, "google|eve-sub",
            email: null, emailVerified: false, extraMetadata: null,
            cancellationToken: default);

        Assert.IsType<LinkOutcome.AlreadyLinkedToYou>(outcome);
    }

    [Fact]
    public async Task Unlink_with_multiple_credentials_removes_one_and_audits()
    {
        var userId = await SeedUserAsync("fran");
        await SeedCredentialAsync(userId, CredentialProvider.Ssh, "SHA256:fran-key");
        await SeedCredentialAsync(userId, CredentialProvider.Google, "google|fran-sub");

        long googleId;
        await using (var db = NewDb())
        {
            googleId = (await db.IdentityCredentials.SingleAsync(c => c.Provider == CredentialProvider.Google)).Id;
        }

        await using var dbAct = NewDb();
        var sut = NewSut(dbAct);
        var outcome = await sut.UnlinkAsync(userId, googleId, default);

        Assert.IsType<UnlinkOutcome.Removed>(outcome);
        Assert.Equal(1, await dbAct.IdentityCredentials.CountAsync(c => c.UserId == userId));
        Assert.NotNull(await dbAct.AuditLogs.SingleOrDefaultAsync(a => a.Action == "identity.unlinked"));
    }

    [Fact]
    public async Task Unlink_refuses_when_only_one_credential_remains()
    {
        var userId = await SeedUserAsync("only");
        await SeedCredentialAsync(userId, CredentialProvider.Ssh, "SHA256:only-key");

        long credId;
        await using (var db = NewDb())
        {
            credId = (await db.IdentityCredentials.SingleAsync()).Id;
        }

        await using var dbAct = NewDb();
        var sut = NewSut(dbAct);
        var outcome = await sut.UnlinkAsync(userId, credId, default);

        Assert.IsType<UnlinkOutcome.RefusedLastCredential>(outcome);
        Assert.Equal(1, await dbAct.IdentityCredentials.CountAsync());
    }

    [Fact]
    public async Task Unlink_returns_NotFound_when_credential_belongs_to_someone_else()
    {
        var aliceId = await SeedUserAsync("alice");
        await SeedCredentialAsync(aliceId, CredentialProvider.Google, "google|alice");
        var bobId = await SeedUserAsync("bob");

        long aliceCredId;
        await using (var db = NewDb())
        {
            aliceCredId = (await db.IdentityCredentials.SingleAsync()).Id;
        }

        await using var dbAct = NewDb();
        var sut = NewSut(dbAct);
        var outcome = await sut.UnlinkAsync(bobId, aliceCredId, default);

        Assert.IsType<UnlinkOutcome.NotFound>(outcome);
    }

    [Theory]
    [InlineData("nick@night.ms", "nick")]
    [InlineData("Bob.Smith@example.com", "BobSmith")]
    [InlineData("ab@x.com", null)]                 // too short after sanitization
    [InlineData("", null)]
    [InlineData(null, null)]
    [InlineData("a_very_long_local_part_that_exceeds_thirty_two_chars@x.com", "a_very_long_local_part_that_exce")]
    public void SuggestHandle_strips_disallowed_chars_and_truncates(string? email, string? expected)
    {
        Assert.Equal(expected, IdentityResolutionService.SuggestHandle(email));
    }
}
