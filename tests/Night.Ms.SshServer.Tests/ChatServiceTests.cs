using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

public class ChatServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private IServiceProvider? _serviceProvider;
    private ChatService? _sut;

    public ChatServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        var services = new ServiceCollection();
        services.AddScoped(_ => new AppDbContext(_dbOptions));
        _serviceProvider = services.BuildServiceProvider();
        _sut = new ChatService(_serviceProvider);
    }

    public Task DisposeAsync()
    {
        (_serviceProvider as IDisposable)?.Dispose();
        return Task.CompletedTask;
    }

    private async Task<User> SeedUserAsync(string handle, bool isBanned = false)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User
        {
            Handle = handle,
            CreatedAt = DateTimeOffset.UtcNow,
            IsBanned = isBanned,
        };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user;
    }

    // ----- public channels -----

    [Fact]
    public async Task JoinPublic_creates_channel_when_missing()
    {
        var actor = await SeedUserAsync("nick");

        var result = await _sut!.JoinPublicChannelAsync("#random", actor.Id, default);

        var created = Assert.IsType<ChatService.JoinResult.Created>(result);
        Assert.Equal("random", created.Channel.Name);
        Assert.False(created.Channel.IsPrivate);
        Assert.Equal(actor.Id, created.Channel.CreatedById);
    }

    [Fact]
    public async Task JoinPublic_finds_existing_channel_on_second_join()
    {
        var actor = await SeedUserAsync("nick");

        var first = await _sut!.JoinPublicChannelAsync("#random", actor.Id, default);
        var second = await _sut!.JoinPublicChannelAsync("#random", actor.Id, default);

        var createdId = Assert.IsType<ChatService.JoinResult.Created>(first).Channel.Id;
        var joinedId = Assert.IsType<ChatService.JoinResult.Joined>(second).Channel.Id;
        Assert.Equal(createdId, joinedId);
    }

    [Fact]
    public async Task JoinPublic_normalizes_name_lowercase_and_strips_hash()
    {
        var actor = await SeedUserAsync("nick");

        var withHash = await _sut!.JoinPublicChannelAsync("#RANDOM", actor.Id, default);
        var withoutHash = await _sut!.JoinPublicChannelAsync("random", actor.Id, default);

        var createdId = Assert.IsType<ChatService.JoinResult.Created>(withHash).Channel.Id;
        var joinedId = Assert.IsType<ChatService.JoinResult.Joined>(withoutHash).Channel.Id;
        Assert.Equal(createdId, joinedId);
    }

    [Fact]
    public async Task JoinPublic_rejects_invalid_names()
    {
        var actor = await SeedUserAsync("nick");

        Assert.IsType<ChatService.JoinResult.InvalidName>(
            await _sut!.JoinPublicChannelAsync("a", actor.Id, default));     // too short
        Assert.IsType<ChatService.JoinResult.InvalidName>(
            await _sut!.JoinPublicChannelAsync(new string('x', 33), actor.Id, default));  // too long
        Assert.IsType<ChatService.JoinResult.InvalidName>(
            await _sut!.JoinPublicChannelAsync("has space", actor.Id, default));
        Assert.IsType<ChatService.JoinResult.InvalidName>(
            await _sut!.JoinPublicChannelAsync("dm-anything", actor.Id, default));  // reserved prefix
    }

    [Fact]
    public async Task JoinPublic_denies_when_a_private_channel_already_owns_the_name()
    {
        var actor = await SeedUserAsync("nick");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.Channels.Add(new Channel { Name = "secrets", IsPrivate = true, CreatedAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();
        }

        var result = await _sut!.JoinPublicChannelAsync("#secrets", actor.Id, default);

        Assert.IsType<ChatService.JoinResult.Denied>(result);
    }

    // ----- DM channels -----

    [Fact]
    public async Task DmChannelName_is_deterministic_regardless_of_initiator()
    {
        Assert.Equal(ChatService.DmChannelName("alice", "bob"), ChatService.DmChannelName("bob", "alice"));
        Assert.Equal("dm-alice-bob", ChatService.DmChannelName("Bob", "Alice"));
    }

    [Fact]
    public async Task JoinDm_creates_a_private_channel_with_two_members()
    {
        var nick = await SeedUserAsync("nick");
        var alice = await SeedUserAsync("alice");

        var result = await _sut!.JoinDmAsync(nick, "alice", default);

        var created = Assert.IsType<ChatService.JoinResult.Created>(result);
        Assert.True(created.Channel.IsPrivate);
        Assert.Equal("dm-alice-nick", created.Channel.Name);

        await using var db = new AppDbContext(_dbOptions!);
        var members = await db.ChannelMembers.Where(m => m.ChannelId == created.Channel.Id).ToListAsync();
        Assert.Equal(2, members.Count);
        Assert.Contains(members, m => m.UserId == nick.Id);
        Assert.Contains(members, m => m.UserId == alice.Id);
    }

    [Fact]
    public async Task JoinDm_is_idempotent_for_the_same_pair()
    {
        var nick = await SeedUserAsync("nick");
        var alice = await SeedUserAsync("alice");

        var first = await _sut!.JoinDmAsync(nick, "alice", default);
        var second = await _sut!.JoinDmAsync(alice, "nick", default);  // reverse direction

        var createdId = Assert.IsType<ChatService.JoinResult.Created>(first).Channel.Id;
        var joinedId = Assert.IsType<ChatService.JoinResult.Joined>(second).Channel.Id;
        Assert.Equal(createdId, joinedId);

        await using var db = new AppDbContext(_dbOptions!);
        var memberCount = await db.ChannelMembers.CountAsync(m => m.ChannelId == createdId);
        Assert.Equal(2, memberCount);  // still just two — second call didn't double up
    }

    [Fact]
    public async Task JoinDm_rejects_self_dm()
    {
        var nick = await SeedUserAsync("nick");

        var result = await _sut!.JoinDmAsync(nick, "nick", default);

        Assert.IsType<ChatService.JoinResult.Denied>(result);
    }

    [Fact]
    public async Task JoinDm_returns_UserNotFound_for_unknown_handle()
    {
        var nick = await SeedUserAsync("nick");

        var result = await _sut!.JoinDmAsync(nick, "ghost", default);

        var notFound = Assert.IsType<ChatService.JoinResult.UserNotFound>(result);
        Assert.Equal("ghost", notFound.Handle);
    }

    [Fact]
    public async Task JoinDm_denies_DM_to_a_banned_user()
    {
        var nick = await SeedUserAsync("nick");
        await SeedUserAsync("troll", isBanned: true);

        var result = await _sut!.JoinDmAsync(nick, "troll", default);

        Assert.IsType<ChatService.JoinResult.Denied>(result);
    }

    // ----- access -----

    [Fact]
    public async Task CanAccess_returns_true_for_public_channels_for_anyone()
    {
        var actor = await SeedUserAsync("nick");
        var stranger = await SeedUserAsync("stranger");
        var public_ = ((ChatService.JoinResult.Created)await _sut!
            .JoinPublicChannelAsync("#open", actor.Id, default)).Channel;

        Assert.True(await _sut!.CanAccessAsync(public_.Id, actor.Id, default));
        Assert.True(await _sut!.CanAccessAsync(public_.Id, stranger.Id, default));
    }

    [Fact]
    public async Task CanAccess_gates_private_channels_by_membership()
    {
        var nick = await SeedUserAsync("nick");
        var alice = await SeedUserAsync("alice");
        var stranger = await SeedUserAsync("stranger");
        var dm = ((ChatService.JoinResult.Created)await _sut!
            .JoinDmAsync(nick, "alice", default)).Channel;

        Assert.True(await _sut!.CanAccessAsync(dm.Id, nick.Id, default));
        Assert.True(await _sut!.CanAccessAsync(dm.Id, alice.Id, default));
        Assert.False(await _sut!.CanAccessAsync(dm.Id, stranger.Id, default));
    }

    [Fact]
    public async Task CanAccess_returns_false_for_unknown_channel_id()
    {
        var actor = await SeedUserAsync("nick");
        Assert.False(await _sut!.CanAccessAsync(channelId: 999_999, actor.Id, default));
    }
}
