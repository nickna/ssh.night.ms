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
    private InMemoryRealtimeBus? _bus;
    private ChatMutationService? _sut;

    public ChatMutationServiceThreadsFtsTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        _bus = new InMemoryRealtimeBus();
        _sut = new ChatMutationService(new TestDbContextFactory(_dbOptions), _bus);
    }

    public Task DisposeAsync() => Task.CompletedTask;

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
        await _sut!.DeleteAsync(reply.Id, u.Id, false, default);

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
        await _sut!.DeleteAsync(msg.Id, u.Id, false, default);

        var hits = await _sut.SearchAsync(c.Id, "secret", limit: 10, default);
        Assert.Empty(hits);
    }

    [Fact]
    public async Task ListThread_returns_root_and_replies_in_chronological_order()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var root = await SeedMessageAsync(u, c, "lets talk about widgets", at: DateTimeOffset.UtcNow.AddMinutes(-10));
        await SeedMessageAsync(u, c, "first reply",  parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-9));
        await SeedMessageAsync(u, c, "second reply", parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-8));
        await SeedMessageAsync(u, c, "third reply",  parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-7));

        var view = await _sut!.ListThreadAsync(root.Id, default);
        Assert.NotNull(view.Root);
        Assert.Equal(root.Id, view.Root!.Id);
        Assert.Equal(3, view.Replies.Count);
        Assert.Equal("first reply",  view.Replies[0].Body);
        Assert.Equal("second reply", view.Replies[1].Body);
        Assert.Equal("third reply",  view.Replies[2].Body);
    }

    [Fact]
    public async Task ListThread_returns_null_root_when_message_missing()
    {
        var view = await _sut!.ListThreadAsync(rootMessageId: 999_999, default);
        Assert.Null(view.Root);
        Assert.Empty(view.Replies);
    }

    [Fact]
    public async Task ListThread_excludes_deleted_replies()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var root = await SeedMessageAsync(u, c, "root", at: DateTimeOffset.UtcNow.AddMinutes(-5));
        var keep = await SeedMessageAsync(u, c, "keeps me", parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-4));
        var del  = await SeedMessageAsync(u, c, "tombstone", parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-3));
        await _sut!.DeleteAsync(del.Id, u.Id, false, default);

        var view = await _sut.ListThreadAsync(root.Id, default);
        Assert.Single(view.Replies);
        Assert.Equal(keep.Id, view.Replies[0].Id);
    }

    [Fact]
    public async Task ListThread_does_not_return_other_threads_replies()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var root  = await SeedMessageAsync(u, c, "thread A", at: DateTimeOffset.UtcNow.AddMinutes(-5));
        var other = await SeedMessageAsync(u, c, "thread B", at: DateTimeOffset.UtcNow.AddMinutes(-4));
        await SeedMessageAsync(u, c, "child of A", parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-3));
        await SeedMessageAsync(u, c, "child of B", parentMessageId: other.Id, at: DateTimeOffset.UtcNow.AddMinutes(-2));

        var view = await _sut!.ListThreadAsync(root.Id, default);
        Assert.Single(view.Replies);
        Assert.Equal("child of A", view.Replies[0].Body);
    }

    [Fact]
    public async Task ListThread_returns_root_with_no_replies()
    {
        var (u, c) = await SeedUserAndChannelAsync();
        var root = await SeedMessageAsync(u, c, "lonely", at: DateTimeOffset.UtcNow);

        var view = await _sut!.ListThreadAsync(root.Id, default);
        Assert.NotNull(view.Root);
        Assert.Equal(root.Id, view.Root!.Id);
        Assert.Empty(view.Replies);
    }

    [Fact]
    public async Task ListThread_returns_tombstoned_root_with_replies_intact()
    {
        // A deleted root still has its row, so the thread is still openable. The renderer
        // is responsible for painting "(deleted)" — the service hands the row back as-is.
        var (u, c) = await SeedUserAndChannelAsync();
        var root = await SeedMessageAsync(u, c, "to be removed", at: DateTimeOffset.UtcNow.AddMinutes(-5));
        await SeedMessageAsync(u, c, "still here", parentMessageId: root.Id, at: DateTimeOffset.UtcNow.AddMinutes(-4));
        await _sut!.DeleteAsync(root.Id, u.Id, false, default);

        var view = await _sut.ListThreadAsync(root.Id, default);
        Assert.NotNull(view.Root);
        Assert.NotNull(view.Root!.DeletedAt);
        Assert.Single(view.Replies);
    }
}
