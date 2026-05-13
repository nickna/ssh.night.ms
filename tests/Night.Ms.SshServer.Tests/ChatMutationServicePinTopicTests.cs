using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Persistence;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

public class ChatMutationServicePinTopicTests : IClassFixture<PostgresFixture>, IAsyncLifetime
{
    private readonly PostgresFixture _fixture;
    private DbContextOptions<AppDbContext>? _dbOptions;
    private ServiceProvider? _services;
    private InMemoryRealtimeBus? _bus;
    private ChatMutationService? _sut;

    public ChatMutationServicePinTopicTests(PostgresFixture fixture) => _fixture = fixture;

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

    private async Task<(User u, Channel c, ChatMessage m)> SeedAsync(string handle, string body, bool createdByUser = true)
    {
        await using var db = new AppDbContext(_dbOptions!);
        var user = new User { Handle = handle, CreatedAt = DateTimeOffset.UtcNow };
        db.Users.Add(user);
        await db.SaveChangesAsync();

        var channel = new Channel
        {
            Name = "lobby",
            IsPrivate = false,
            CreatedAt = DateTimeOffset.UtcNow,
            CreatedById = createdByUser ? user.Id : null,
        };
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
    public async Task Pin_sets_flag_and_publishes()
    {
        var (u, _, m) = await SeedAsync("alice", "important");
        var result = await _sut!.PinAsync(m.Id, u.Id, default);

        Assert.IsType<ChatMutationService.Result.Ok>(result);
        await using var db = new AppDbContext(_dbOptions!);
        Assert.True((await db.ChatMessages.FirstAsync(x => x.Id == m.Id)).IsPinned);
        Assert.Equal(ChatEventKind.Pin,
            JsonSerializer.Deserialize<ChatEnvelope>(_bus!.Published[0].Payload)!.Kind);
    }

    [Fact]
    public async Task Pinning_already_pinned_is_idempotent()
    {
        var (u, _, m) = await SeedAsync("alice", "important");
        await _sut!.PinAsync(m.Id, u.Id, default);
        var publishedBefore = _bus!.Published.Count;
        var result = await _sut.PinAsync(m.Id, u.Id, default);

        Assert.IsType<ChatMutationService.Result.Ok>(result);
        // No second publish — nothing changed.
        Assert.Equal(publishedBefore, _bus.Published.Count);
    }

    [Fact]
    public async Task Unpin_clears_flag()
    {
        var (u, _, m) = await SeedAsync("alice", "important");
        await _sut!.PinAsync(m.Id, u.Id, default);
        await _sut.UnpinAsync(m.Id, u.Id, default);

        await using var db = new AppDbContext(_dbOptions!);
        Assert.False((await db.ChatMessages.FirstAsync(x => x.Id == m.Id)).IsPinned);
    }

    [Fact]
    public async Task Pin_of_deleted_message_is_forbidden()
    {
        var (u, _, m) = await SeedAsync("alice", "bye");
        await _sut!.DeleteAsync(m.Id, u.Id, default);
        var result = await _sut.PinAsync(m.Id, u.Id, default);

        Assert.IsType<ChatMutationService.Result.Forbidden>(result);
    }

    [Fact]
    public async Task ListPins_returns_pinned_only_newest_first()
    {
        var (u, c, _) = await SeedAsync("alice", "first");
        // Seed a second message and pin both.
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.ChatMessages.Add(new ChatMessage
            {
                ChannelId = c.Id, UserId = u.Id, Body = "second",
                CreatedAt = DateTimeOffset.UtcNow.AddSeconds(1),
            });
            await db.SaveChangesAsync();
        }
        await using var db2 = new AppDbContext(_dbOptions!);
        var msgs = await db2.ChatMessages.OrderBy(x => x.CreatedAt).ToListAsync();
        await _sut!.PinAsync(msgs[0].Id, u.Id, default);
        await _sut.PinAsync(msgs[1].Id, u.Id, default);

        var pins = await _sut.ListPinsAsync(c.Id, default);
        Assert.Equal(2, pins.Count);
        Assert.Equal("second", pins[0].Body); // newest pinned first
    }

    [Fact]
    public async Task SetTopic_updates_channel_and_publishes()
    {
        var (u, c, _) = await SeedAsync("alice", "x");
        var result = await _sut!.SetTopicAsync(c.Id, u.Id, u.Handle, "now featuring: stuff", default);

        Assert.IsType<ChatMutationService.Result.Ok>(result);
        await using var db = new AppDbContext(_dbOptions!);
        Assert.Equal("now featuring: stuff", (await db.Channels.FirstAsync(x => x.Id == c.Id)).Topic);
        Assert.Equal(ChatEventKind.Topic,
            JsonSerializer.Deserialize<ChatEnvelope>(_bus!.Published[0].Payload)!.Kind);
    }

    [Fact]
    public async Task SetTopic_by_non_creator_is_forbidden()
    {
        var (alice, c, _) = await SeedAsync("alice", "x");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.Users.Add(new User { Handle = "bob", CreatedAt = DateTimeOffset.UtcNow });
            await db.SaveChangesAsync();
        }
        await using var db2 = new AppDbContext(_dbOptions!);
        var bob = await db2.Users.FirstAsync(x => x.Handle == "bob");

        var result = await _sut!.SetTopicAsync(c.Id, bob.Id, bob.Handle, "hijacked", default);
        Assert.IsType<ChatMutationService.Result.Forbidden>(result);
    }

    [Fact]
    public async Task SetTopic_empty_clears_topic()
    {
        var (u, c, _) = await SeedAsync("alice", "x");
        await _sut!.SetTopicAsync(c.Id, u.Id, u.Handle, "existing", default);
        await _sut.SetTopicAsync(c.Id, u.Id, u.Handle, "   ", default);

        await using var db = new AppDbContext(_dbOptions!);
        Assert.Null((await db.Channels.FirstAsync(x => x.Id == c.Id)).Topic);
    }

    [Fact]
    public async Task SetTopic_too_long_is_invalid()
    {
        var (u, c, _) = await SeedAsync("alice", "x");
        var result = await _sut!.SetTopicAsync(c.Id, u.Id, u.Handle, new string('x', 250), default);
        Assert.IsType<ChatMutationService.Result.Invalid>(result);
    }

    [Fact]
    public async Task Search_matches_are_case_insensitive()
    {
        // Postgres FTS lowercases tokens, so a search for "build" matches "BUILD" but not
        // the substring inside "rebuild" — different word, different token. That's a
        // semantic change from the old ILIKE-based search.
        var (u, c, _) = await SeedAsync("alice", "fixed the BUILD today");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.ChatMessages.Add(new ChatMessage
            {
                ChannelId = c.Id, UserId = u.Id, Body = "rebuild script tomorrow",
                CreatedAt = DateTimeOffset.UtcNow.AddSeconds(1),
            });
            db.ChatMessages.Add(new ChatMessage
            {
                ChannelId = c.Id, UserId = u.Id, Body = "completely unrelated",
                CreatedAt = DateTimeOffset.UtcNow.AddSeconds(2),
            });
            await db.SaveChangesAsync();
        }

        var hits = await _sut!.SearchAsync(c.Id, "build", limit: 10, default);
        Assert.Single(hits);
        Assert.Equal("fixed the BUILD today", hits[0].Body);
    }

    [Fact]
    public async Task Search_excludes_deleted_messages()
    {
        var (u, c, m) = await SeedAsync("alice", "needle in the haystack");
        await _sut!.DeleteAsync(m.Id, u.Id, default);
        var hits = await _sut.SearchAsync(c.Id, "needle", limit: 10, default);
        Assert.Empty(hits);
    }

    [Fact]
    public async Task Search_escapes_lik_wildcards_in_input()
    {
        var (u, c, _) = await SeedAsync("alice", "literal_underscore here");
        await using (var db = new AppDbContext(_dbOptions!))
        {
            db.ChatMessages.Add(new ChatMessage
            {
                ChannelId = c.Id, UserId = u.Id, Body = "no underscores at all",
                CreatedAt = DateTimeOffset.UtcNow.AddSeconds(1),
            });
            await db.SaveChangesAsync();
        }
        // The "_" in the search term should match a literal underscore, not "any char".
        var hits = await _sut!.SearchAsync(c.Id, "literal_", limit: 10, default);
        Assert.Single(hits);
    }
}
