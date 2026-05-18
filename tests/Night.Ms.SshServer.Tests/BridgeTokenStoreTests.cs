using Night.Ms.SshServer.Auth;

namespace Night.Ms.SshServer.Tests;

public sealed class BridgeTokenStoreTests : IClassFixture<RedisFixture>, IAsyncLifetime
{
    private readonly RedisFixture _redis;
    private readonly RedisBridgeTokenStore _store;

    public BridgeTokenStoreTests(RedisFixture redis)
    {
        _redis = redis;
        _store = new RedisBridgeTokenStore(redis.Multiplexer);
    }

    // Each test starts from a clean Redis so the per-user rate-limit counter from one test
    // doesn't bleed into the next. FLUSHDB is fine because the fixture is scoped per test
    // class and nothing else shares the database within a class.
    public async Task InitializeAsync() => await _redis.Multiplexer.GetServers()[0].FlushDatabaseAsync();
    public Task DisposeAsync() => Task.CompletedTask;

    [Fact]
    public async Task IssueAsync_returns_distinct_tokens_per_call()
    {
        var a = await _store.IssueAsync(userId: 1, default);
        var b = await _store.IssueAsync(userId: 1, default);
        Assert.NotNull(a);
        Assert.NotNull(b);
        Assert.NotEqual(a, b);
    }

    [Fact]
    public async Task RedeemAsync_returns_userId_for_fresh_token()
    {
        var token = await _store.IssueAsync(userId: 42, default);
        Assert.NotNull(token);
        var userId = await _store.RedeemAsync(token!, default);
        Assert.Equal(42, userId);
    }

    [Fact]
    public async Task RedeemAsync_returns_null_after_first_redeem()
    {
        var token = await _store.IssueAsync(userId: 7, default);
        Assert.NotNull(token);
        var first = await _store.RedeemAsync(token!, default);
        var second = await _store.RedeemAsync(token!, default);
        Assert.Equal(7, first);
        Assert.Null(second);
    }

    [Fact]
    public async Task RedeemAsync_returns_null_for_unknown_token()
    {
        var nonsense = "this-was-never-issued-abcdef";
        var result = await _store.RedeemAsync(nonsense, default);
        Assert.Null(result);
    }

    [Fact]
    public async Task RedeemAsync_returns_null_for_empty_token()
    {
        Assert.Null(await _store.RedeemAsync(string.Empty, default));
    }

    [Fact]
    public async Task IssueAsync_returns_null_after_per_user_cap()
    {
        // 5 is the cap (MaxPerHour). The 6th attempt within the window should refuse.
        for (var i = 0; i < 5; i++)
        {
            var t = await _store.IssueAsync(userId: 99, default);
            Assert.NotNull(t);
        }
        var refused = await _store.IssueAsync(userId: 99, default);
        Assert.Null(refused);
    }

    [Fact]
    public async Task IssueAsync_rate_limit_is_per_user_not_global()
    {
        for (var i = 0; i < 5; i++)
        {
            Assert.NotNull(await _store.IssueAsync(userId: 100, default));
        }
        Assert.Null(await _store.IssueAsync(userId: 100, default));

        // A different user should still be unaffected.
        Assert.NotNull(await _store.IssueAsync(userId: 101, default));
    }
}
