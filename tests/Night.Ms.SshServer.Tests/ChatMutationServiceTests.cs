using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

public class ChatMutationServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private InMemoryRealtimeBus? _bus;
    private ChatMutationService? _sut;

    public ChatMutationServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        _bus = new InMemoryRealtimeBus();
        _sut = new ChatMutationService(new TestDbContextFactory(_dbOptions), _bus);
    }

    public Task DisposeAsync() => Task.CompletedTask;

    private async Task<(User user, Channel channel, ChatMessage msg)> SeedMessageAsync(string handle, string body)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();

        var channel = new Channel { Name = "lobby", IsPrivate = false, CreatedAt = DateTimeOffset.UtcNow };
        db.Channels.Add(channel);
        await db.SaveChangesAsync();

        var msg = new ChatMessage
        {
            ChannelId = channel.Id,
            UserId = user.Id,
            Body = body,
            CreatedAt = DateTimeOffset.UtcNow,
        };
        db.ChatMessages.Add(msg);
        await db.SaveChangesAsync();
        return (user, channel, msg);
    }

    [Fact]
    public async Task Edit_updates_body_and_sets_edited_timestamp()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "original");
        var result = await _sut!.EditAsync(msg.Id, user.Id, "edited body", default);

        Assert.IsType<ChatOpResult.Ok>(result);
        await using var db = new AppDbContext(_dbOptions!);
        var updated = await db.ChatMessages.FirstAsync(m => m.Id == msg.Id);
        Assert.Equal("edited body", updated.Body);
        Assert.NotNull(updated.EditedAt);
    }

    [Fact]
    public async Task Edit_publishes_edit_envelope()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "original");
        await _sut!.EditAsync(msg.Id, user.Id, "edited body", default);

        Assert.Single(_bus!.Published);
        var envelope = JsonSerializer.Deserialize<ChatEnvelope>(_bus.Published[0].Payload);
        Assert.NotNull(envelope);
        Assert.Equal(ChatEventKind.Edit, envelope!.Kind);
    }

    [Fact]
    public async Task Edit_by_non_author_is_forbidden()
    {
        var (_, _, msg) = await SeedMessageAsync("alice", "original");
        // bob seeds his own row to get an ID.
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.Users.Add(new User { Handle = "bob", CreatedAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();
        }
        await using var db2 = new AppDbContext(_dbOptions!);
        var bob = await db2.Users.FirstAsync(u => u.Handle == "bob");

        var result = await _sut!.EditAsync(msg.Id, bob.Id, "hijacked", default);
        Assert.IsType<ChatOpResult.Forbidden>(result);

        var unchanged = await db2.ChatMessages.AsNoTracking().FirstAsync(m => m.Id == msg.Id);
        Assert.Equal("original", unchanged.Body);
    }

    [Fact]
    public async Task Edit_of_unknown_message_returns_NotFound()
    {
        var result = await _sut!.EditAsync(99999, 1, "x", default);
        Assert.IsType<ChatOpResult.NotFound>(result);
    }

    [Fact]
    public async Task Edit_with_empty_body_returns_Invalid()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "original");
        var result = await _sut!.EditAsync(msg.Id, user.Id, "   ", default);
        Assert.IsType<ChatOpResult.Invalid>(result);
    }

    [Fact]
    public async Task Delete_tombstones_message_without_removing_row()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "bye");
        var result = await _sut!.DeleteAsync(msg.Id, user.Id, default);

        Assert.IsType<ChatOpResult.Ok>(result);
        await using var db = new AppDbContext(_dbOptions!);
        var tombstoned = await db.ChatMessages.FirstAsync(m => m.Id == msg.Id);
        Assert.NotNull(tombstoned.DeletedAt);
        Assert.Equal("bye", tombstoned.Body); // body kept for moderation audit
    }

    [Fact]
    public async Task Delete_by_non_author_is_forbidden()
    {
        var (_, _, msg) = await SeedMessageAsync("alice", "secret");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.Users.Add(new User { Handle = "mallory", CreatedAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();
        }
        await using var db2 = new AppDbContext(_dbOptions!);
        var mallory = await db2.Users.FirstAsync(u => u.Handle == "mallory");

        var result = await _sut!.DeleteAsync(msg.Id, mallory.Id, default);
        Assert.IsType<ChatOpResult.Forbidden>(result);
    }

    [Fact]
    public async Task React_inserts_row_and_publishes()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "ship it");
        var result = await _sut!.ReactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);

        Assert.IsType<ChatOpResult.Ok>(result);
        await using var db = new AppDbContext(_dbOptions!);
        Assert.Single(db.MessageReactions);
        Assert.Equal(ChatEventKind.React,
            JsonSerializer.Deserialize<ChatEnvelope>(_bus!.Published[0].Payload)!.Kind);
    }

    [Fact]
    public async Task React_duplicate_is_idempotent()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "ship it");
        await _sut!.ReactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);
        var result2 = await _sut!.ReactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);
        Assert.IsType<ChatOpResult.Ok>(result2);

        await using var db = new AppDbContext(_dbOptions!);
        Assert.Single(db.MessageReactions);
    }

    [Fact]
    public async Task Unreact_removes_existing_row()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "ship it");
        await _sut!.ReactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);
        var result = await _sut.UnreactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);

        Assert.IsType<ChatOpResult.Ok>(result);
        await using var db = new AppDbContext(_dbOptions!);
        Assert.Empty(db.MessageReactions);
    }

    [Fact]
    public async Task Unreact_of_nonexistent_reaction_returns_NotFound()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "ship it");
        var result = await _sut!.UnreactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);
        Assert.IsType<ChatOpResult.NotFound>(result);
    }

    [Fact]
    public async Task React_on_deleted_message_is_forbidden()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "bye");
        await _sut!.DeleteAsync(msg.Id, user.Id, default);
        // Clear publish log so the assertion below targets only the React attempt.
        var result = await _sut.ReactAsync(msg.Id, user.Id, "alice", "\U0001F525", default);
        Assert.IsType<ChatOpResult.Forbidden>(result);
    }

    [Fact]
    public async Task Snapshot_reactions_returns_grouped_per_message()
    {
        var (user, _, msg) = await SeedMessageAsync("alice", "great work");
        await _sut!.ReactAsync(msg.Id, user.Id, "alice", "\U0001F680", default);
        await _sut.ReactAsync(msg.Id, user.Id, "alice", "\U0001F525", default);

        var snapshot = await _sut.SnapshotReactionsAsync(new[] { msg.Id }, default);
        Assert.Single(snapshot);
        Assert.Equal(2, snapshot[msg.Id].Count);
    }
}
