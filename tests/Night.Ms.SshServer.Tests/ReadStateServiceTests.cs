using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

public class ReadStateServiceTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private ServiceProvider? _services;
    private ReadStateService? _sut;

    public ReadStateServiceTests(PostgresFixture fixture) => _fixture = fixture;

    public async Task InitializeAsync()
    {
        _dbOptions = await _fixture.CreateFreshDatabaseAsync();
        var services = new ServiceCollection();
        services.AddScoped(_ => new AppDbContext(_dbOptions));
        _services = services.BuildServiceProvider();
        _sut = new ReadStateService(_services);
    }

    public Task DisposeAsync()
    {
        _services?.Dispose();
        return Task.CompletedTask;
    }

    private async Task<User> SeedUserAsync(string handle)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();
        return user;
    }

    private async Task<Channel> SeedChannelAsync(string name, User? creator = null)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var c = new Channel
        {
            Name = name,
            IsPrivate = false,
            CreatedAt = DateTimeOffset.UtcNow,
            CreatedById = creator?.Id,
        };
        db.Channels.Add(c);
        await db.SaveChangesAsync();
        return c;
    }

    private async Task<ChatMessage> SeedMessageAsync(Channel channel, User author, string body)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var m = new ChatMessage
        {
            ChannelId = channel.Id,
            UserId = author.Id,
            Body = body,
            CreatedAt = DateTimeOffset.UtcNow,
        };
        db.ChatMessages.Add(m);
        await db.SaveChangesAsync();
        return m;
    }

    [Fact]
    public async Task MarkRead_inserts_row_when_missing()
    {
        var user = await SeedUserAsync("alice");
        var channel = await SeedChannelAsync("lobby");
        var msg = await SeedMessageAsync(channel, user, "hi");

        await _sut!.MarkReadAsync(user.Id, channel.Id, msg.Id, default);

        await using var db = new AppDbContext(_dbOptions!);
        var row = await db.ChannelReads.SingleAsync();
        Assert.Equal(msg.Id, row.LastReadMessageId);
    }

    [Fact]
    public async Task MarkRead_advances_pointer_forward_only()
    {
        var user = await SeedUserAsync("alice");
        var channel = await SeedChannelAsync("lobby");
        var older = await SeedMessageAsync(channel, user, "old");
        var newer = await SeedMessageAsync(channel, user, "new");

        await _sut!.MarkReadAsync(user.Id, channel.Id, newer.Id, default);
        // Try to regress to the older id — should refuse.
        await _sut.MarkReadAsync(user.Id, channel.Id, older.Id, default);

        await using var db = new AppDbContext(_dbOptions!);
        var row = await db.ChannelReads.SingleAsync();
        Assert.Equal(newer.Id, row.LastReadMessageId);
    }

    [Fact]
    public async Task List_includes_lobby_even_without_read_row()
    {
        var user = await SeedUserAsync("alice");
        // Seed a #lobby channel (the seeding done by DatabaseInitializer at runtime is not
        // exercised here, so we set it up ourselves).
        await SeedChannelAsync("lobby");

        var entries = await _sut!.ListForUserAsync(user.Id, default);
        Assert.Contains(entries, e => e.Name == "lobby");
    }

    [Fact]
    public async Task List_returns_unread_count_above_pointer()
    {
        var user = await SeedUserAsync("alice");
        var channel = await SeedChannelAsync("tech");
        var first = await SeedMessageAsync(channel, user, "one");
        await _sut!.MarkReadAsync(user.Id, channel.Id, first.Id, default);
        await SeedMessageAsync(channel, user, "two");
        await SeedMessageAsync(channel, user, "three");

        var entries = await _sut.ListForUserAsync(user.Id, default);
        var tech = entries.Single(e => e.Name == "tech");
        Assert.Equal(2, tech.UnreadCount);
    }

    [Fact]
    public async Task List_orders_unread_channels_before_caught_up_ones()
    {
        var user = await SeedUserAsync("alice");
        await SeedChannelAsync("lobby");
        var caughtUp = await SeedChannelAsync("readme");
        var unread = await SeedChannelAsync("hot");

        // User has read 'readme' to the latest, but 'hot' has new messages they haven't
        // seen. Sort order should float 'hot' to the top.
        var readmeMsg = await SeedMessageAsync(caughtUp, user, "old news");
        await _sut!.MarkReadAsync(user.Id, caughtUp.Id, readmeMsg.Id, default);
        var hotMsg = await SeedMessageAsync(unread, user, "fresh");
        await _sut.MarkReadAsync(user.Id, unread.Id, 0, default); // touch row, leave unread

        var entries = await _sut.ListForUserAsync(user.Id, default);
        var hotIdx = entries.ToList().FindIndex(e => e.Name == "hot");
        var readmeIdx = entries.ToList().FindIndex(e => e.Name == "readme");
        Assert.True(hotIdx < readmeIdx, "channel with unread should sort before caught-up one");
    }

    [Fact]
    public async Task List_excludes_deleted_messages_from_unread_count()
    {
        var user = await SeedUserAsync("alice");
        var channel = await SeedChannelAsync("lobby");
        var msg = await SeedMessageAsync(channel, user, "hi");

        // Mark deleted directly on the row (the service-level delete is exercised
        // elsewhere; here we just need a tombstone).
        await using (var db = new AppDbContext(_dbOptions!))
        {
            var fetched = await db.ChatMessages.SingleAsync();
            fetched.DeletedAt = DateTimeOffset.UtcNow;
            await db.SaveChangesAsync();
        }

        var entries = await _sut!.ListForUserAsync(user.Id, default);
        var lobby = entries.Single(e => e.Name == "lobby");
        Assert.Equal(0, lobby.UnreadCount);
    }
}
