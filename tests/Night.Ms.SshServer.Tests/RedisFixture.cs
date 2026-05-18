using StackExchange.Redis;
using Testcontainers.Redis;

namespace Night.Ms.SshServer.Tests;

// Spins up a real Redis container once per test class. Tests that need isolation can
// FLUSHDB between cases; the store under test usually scopes itself by a prefix anyway.
public sealed class RedisFixture : IAsyncLifetime
{
    private readonly RedisContainer _container = new RedisBuilder("redis:7-alpine").Build();

    public IConnectionMultiplexer Multiplexer { get; private set; } = default!;

    public async Task InitializeAsync()
    {
        await _container.StartAsync();
        // allowAdmin enables FLUSHDB so per-test cleanup works.
        var config = ConfigurationOptions.Parse(_container.GetConnectionString());
        config.AllowAdmin = true;
        Multiplexer = await ConnectionMultiplexer.ConnectAsync(config);
    }

    public async Task DisposeAsync()
    {
        Multiplexer?.Dispose();
        await _container.DisposeAsync();
    }
}
