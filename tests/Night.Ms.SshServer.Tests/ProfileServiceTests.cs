using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

public class ProfileServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private IServiceProvider? _serviceProvider;
    private ProfileService? _sut;

    public ProfileServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        var services = new ServiceCollection();
        services.AddScoped(_ => new AppDbContext(_dbOptions));
        _serviceProvider = services.BuildServiceProvider();
        _sut = new ProfileService(_serviceProvider);
    }

    public Task DisposeAsync()
    {
        (_serviceProvider as IDisposable)?.Dispose();
        return Task.CompletedTask;
    }

    private async Task<User> SeedUserAsync(string handle, bool isSysop = false)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User
        {
            Handle = handle,
            CreatedAt = DateTimeOffset.UtcNow,
            IsSysop = isSysop,
        };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user;
    }

    // ----- GetByHandleAsync -----

    [Fact]
    public async Task GetByHandle_returns_null_for_unknown_user()
    {
        var result = await _sut!.GetByHandleAsync("ghost", default);
        Assert.Null(result);
    }

    [Fact]
    public async Task GetByHandle_returns_snapshot_with_zero_stats_for_fresh_user()
    {
        await SeedUserAsync("nick", isSysop: true);

        var snap = await _sut!.GetByHandleAsync("nick", default);

        Assert.NotNull(snap);
        Assert.Equal("nick", snap!.Handle);
        Assert.True(snap.IsSysop);
        Assert.Equal(0, snap.ChatMessageCount);
        Assert.Equal(0, snap.TopicCount);
        Assert.Equal(0, snap.PostCount);
    }

    [Fact]
    public async Task GetByHandle_computes_derived_stats()
    {
        var nick = await SeedUserAsync("nick");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            var ch = new Channel { Name = "lobby", CreatedAt = DateTimeOffset.UtcNow };
            db.Channels.Add(ch);
            var forum = new Forum { Name = "General", SortOrder = 0 };
            db.Forums.Add(forum);
            await db.SaveChangesAsync();

            // 3 chat messages, 2 topics, 5 posts
            for (var i = 0; i < 3; i++)
                db.ChatMessages.Add(new ChatMessage { ChannelId = ch.Id, UserId = nick.Id, Body = $"msg {i}", CreatedAt = DateTimeOffset.UtcNow });
            for (var i = 0; i < 2; i++)
                db.Topics.Add(new Topic { ForumId = forum.Id, Title = $"t {i}", CreatedById = nick.Id, CreatedAt = DateTimeOffset.UtcNow, LastPostAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();

            var firstTopicId = await db.Topics.OrderBy(t => t.Id).Select(t => t.Id).FirstAsync();
            for (var i = 0; i < 5; i++)
                db.Posts.Add(new Post { TopicId = firstTopicId, Body = $"p {i}", CreatedById = nick.Id, CreatedAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();
        }

        var snap = await _sut!.GetByHandleAsync("nick", default);

        Assert.Equal(3, snap!.ChatMessageCount);
        Assert.Equal(2, snap.TopicCount);
        Assert.Equal(5, snap.PostCount);
    }

    [Fact]
    public async Task GetByHandle_is_case_insensitive_via_citext()
    {
        await SeedUserAsync("Alice");

        Assert.NotNull(await _sut!.GetByHandleAsync("Alice", default));
        Assert.NotNull(await _sut!.GetByHandleAsync("alice", default));
        Assert.NotNull(await _sut!.GetByHandleAsync("ALICE", default));
    }

    // ----- UpdateAsync -----

    [Fact]
    public async Task Update_persists_all_three_fields()
    {
        var u = await SeedUserAsync("nick");

        var result = await _sut!.UpdateAsync(u.Id, realName: "Nick Q.", location: "Brooklyn, NY", bio: "making a mess", default);

        Assert.True(result.Ok);
        await using var db = new AppDbContext(_dbOptions!);
        var loaded = await db.Users.FindAsync(u.Id);
        Assert.Equal("Nick Q.", loaded!.RealName);
        Assert.Equal("Brooklyn, NY", loaded.Location);
        Assert.Equal("making a mess", loaded.Bio);
    }

    [Fact]
    public async Task Update_treats_whitespace_only_input_as_null()
    {
        var u = await SeedUserAsync("nick");
        // Pre-fill so we can verify the update clears them.
        await _sut!.UpdateAsync(u.Id, "Nick", "NYC", "bio", default);

        var result = await _sut!.UpdateAsync(u.Id, "   ", "", null, default);

        Assert.True(result.Ok);
        await using var db = new AppDbContext(_dbOptions!);
        var loaded = await db.Users.FindAsync(u.Id);
        Assert.Null(loaded!.RealName);
        Assert.Null(loaded.Location);
        Assert.Null(loaded.Bio);
    }

    [Fact]
    public async Task Update_rejects_bio_over_max_length()
    {
        var u = await SeedUserAsync("nick");
        var oversize = new string('x', ProfileService.MaxBioLength + 1);

        var result = await _sut!.UpdateAsync(u.Id, null, null, oversize, default);

        Assert.False(result.Ok);
        Assert.Contains("Bio", result.Error);
    }

    [Fact]
    public async Task Update_rejects_real_name_over_max_length()
    {
        var u = await SeedUserAsync("nick");
        var oversize = new string('x', ProfileService.MaxRealNameLength + 1);

        var result = await _sut!.UpdateAsync(u.Id, oversize, null, null, default);

        Assert.False(result.Ok);
        Assert.Contains("Real name", result.Error);
    }

    [Fact]
    public async Task Update_returns_error_for_unknown_user_id()
    {
        var result = await _sut!.UpdateAsync(userId: 999_999, "x", "y", "z", default);

        Assert.False(result.Ok);
        Assert.Equal("User not found.", result.Error);
    }

    // ----- FormatFinger -----

    [Fact]
    public void FormatFinger_includes_handle_joined_lastseen_and_stats()
    {
        var snap = new ProfileSnapshot(
            UserId: 1,
            Handle: "nick",
            RealName: "Nick Q.",
            Location: "Brooklyn, NY",
            Bio: "making a mess",
            CreatedAt: new DateTimeOffset(2026, 5, 11, 0, 0, 0, TimeSpan.Zero),
            LastSeenAt: new DateTimeOffset(2026, 5, 11, 16, 42, 0, TimeSpan.Zero),
            IsSysop: true,
            ChatMessageCount: 127,
            TopicCount: 3,
            PostCount: 18);

        var rendered = ProfileService.FormatFinger(snap);

        Assert.Contains("finger nick (sysop)", rendered);
        Assert.Contains("Nick Q.", rendered);
        Assert.Contains("Brooklyn, NY", rendered);
        Assert.Contains("making a mess", rendered);
        Assert.Contains("127 chat / 3 topics / 18 posts", rendered);
    }

    [Fact]
    public void FormatFinger_omits_blank_optional_fields()
    {
        var snap = new ProfileSnapshot(
            UserId: 1,
            Handle: "bare",
            RealName: null,
            Location: null,
            Bio: null,
            CreatedAt: DateTimeOffset.UtcNow,
            LastSeenAt: null,
            IsSysop: false,
            ChatMessageCount: 0,
            TopicCount: 0,
            PostCount: 0);

        var rendered = ProfileService.FormatFinger(snap);

        Assert.Contains("finger bare ──", rendered);
        Assert.Contains("<never>", rendered);
        Assert.DoesNotContain("real name", rendered);
        Assert.DoesNotContain("location", rendered);
        Assert.DoesNotContain("bio", rendered);
    }
}
