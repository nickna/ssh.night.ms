using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

public class ChatMutationServiceThreadsFtsTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private ServiceProvider? _services;
    private InMemoryRealtimeBus? _bus;
    private ChatMutationService? _sut;

    public ChatMutationServiceThreadsFtsTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        _bus = new InMemoryRealtimeBus();
        var services = new ServiceCollection();
        services.AddScoped(_ => new AppDbContext(_dbOptions));
        services.AddSingleton<IRealtimeBus>(_bus);
        _services = services.BuildServiceProvider();
        _sut = new ChatMutationService(_services);
    }

    public Task DisposeAsync()
    {
        _services?.Dispose();
        return Task.CompletedTask;
    }

    private async Task<(User u, Channel c)> SeedUserAndChannelAsync()
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = "alice", CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        var channel = new Channel { Name = "lobby", IsPrivate = false, CreatedAt = DateTimeOffset.UtcNow, CreatedById = user.Id };
        db.Channels.Add(channel);
        await db.SaveChangesAsync();
        return (user, channel);
    }

    private async Task<ChatMessage> SeedMessageAsync(User user, Channel channel, string body, long? parentMessageId = null, DateTimeOffset? at = null)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var msg = new ChatMessage
        {
            ChannelId = channel.Id,
            UserId = user.Id,
            Body = body,
            CreatedAt = at ?? DateTimeOffset.UtcNow,
            ParentMessageId = parentMessageId,
        };
        db.ChatMessages.Add(msg);
        await db.SaveChangesAsync();
        return msg;
    }

    [Fact]
    public async Task SnapshotReplyCounts_groups_per_parent()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var parent = await SeedMessageAsync(u, c, "topic");
        await SeedMessageAsync(u, c, "first reply", parentMessageId: parent.Id);
        await SeedMessageAsync(u, c, "second reply", parentMessageId: parent.Id);
        var other = await SeedMessageAsync(u, c, "different thread");
        await SeedMessageAsync(u, c, "lonely reply", parentMessageId: other.Id);

        var counts = await _sut!.SnapshotReplyCountsAsync(new[] { parent.Id, other.Id }, default);
        Assert.Equal(2, counts[parent.Id]);
        Assert.Equal(1, counts[other.Id]);
    }

    [Fact]
    public async Task SnapshotReplyCounts_excludes_deleted_children()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var parent = await SeedMessageAsync(u, c, "topic");
        var reply = await SeedMessageAsync(u, c, "reply", parentMessageId: parent.Id);
        await _sut!.DeleteAsync(reply.Id, u.Id, default);

        var counts = await _sut.SnapshotReplyCountsAsync(new[] { parent.Id }, default);
        Assert.False(counts.ContainsKey(parent.Id));
    }

    [Fact]
    public async Task SnapshotReplyCounts_empty_input_returns_empty_dict()
    {
        var result = await _sut!.SnapshotReplyCountsAsync(Array.Empty<long>(), default);
        Assert.Empty(result);
    }

    [Fact]
    public async Task Search_uses_fts_for_word_match()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        await SeedMessageAsync(u, c, "ship the migration today", at: DateTimeOffset.UtcNow.AddMinutes(-2));
        await SeedMessageAsync(u, c, "completely unrelated", at: DateTimeOffset.UtcNow.AddMinutes(-1));
        await SeedMessageAsync(u, c, "migrations are pending", at: DateTimeOffset.UtcNow);

        var hits = await _sut!.SearchAsync(c.Id, "migration", limit: 10, default);
        Assert.Equal(2, hits.Count); // FTS stems "migration" → matches "migrations" too
    }

    [Fact]
    public async Task Search_supports_negated_terms()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        await SeedMessageAsync(u, c, "build is green", at: DateTimeOffset.UtcNow.AddMinutes(-2));
        await SeedMessageAsync(u, c, "build is red", at: DateTimeOffset.UtcNow.AddMinutes(-1));

        // websearch_to_tsquery: "build -red" matches messages with "build" but not "red".
        var hits = await _sut!.SearchAsync(c.Id, "build -red", limit: 10, default);
        Assert.Single(hits);
        Assert.Equal("build is green", hits[0].Body);
    }

    [Fact]
    public async Task Search_falls_back_to_ilike_for_short_tokens()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        // "x" is one-letter, won't tokenize cleanly under FTS' default config.
        await SeedMessageAsync(u, c, "fix the x issue", at: DateTimeOffset.UtcNow);

        var hits = await _sut!.SearchAsync(c.Id, "x", limit: 10, default);
        Assert.Single(hits);
    }

    [Fact]
    public async Task Search_excludes_deleted_messages_under_fts()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var msg = await SeedMessageAsync(u, c, "secret information", at: DateTimeOffset.UtcNow);
        await _sut!.DeleteAsync(msg.Id, u.Id, default);

        var hits = await _sut.SearchAsync(c.Id, "secret", limit: 10, default);
        Assert.Empty(hits);
    }
}
