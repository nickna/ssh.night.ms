using System.Collections.Concurrent;
using System.Text.Json;

namespace Night.Ms.SshServer.Realtime;

// Singleton fan-out for the system-wide wall broadcast. Earlier shape opened a per-session
// IRealtimeBus.SubscribeAsync (with its own Channel<byte[]> and background Task) so every
// session redundantly deserialized every broadcast and held its own subscriber state. With
// hundreds of concurrent sessions that's N redundant deserialize + dispatch passes for a
// system event that's already global. This subscribes once and invokes each registered
// callback with the parsed DTO.
//
// Hosted service lifecycle: StartAsync kicks the subscription loop on a background Task;
// StopAsync cancels it on shutdown. Subscribe returns an IDisposable that removes the
// callback when disposed — the lobby loop's `using` statement keeps registrations scoped
// to the session lifetime without any explicit unregister call.
public sealed class WallDispatcher : IHostedService
{
    private readonly IRealtimeBus _bus;
    private readonly ILogger<WallDispatcher> _logger;
    private readonly ConcurrentDictionary<Guid, Action<WallBroadcastDto>> _subscribers = new();
    private CancellationTokenSource? _cts;
    private Task? _loop;

    public WallDispatcher(IRealtimeBus bus, ILogger<WallDispatcher> logger)
    {
        _bus = bus;
        _logger = logger;
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        _cts = new CancellationTokenSource();
        _loop = Task.Run(() => RunAsync(_cts.Token));
        return Task.CompletedTask;
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        _cts?.Cancel();
        if (_loop is not null)
        {
            try { await _loop.WaitAsync(cancellationToken).ConfigureAwait(false); }
            catch (OperationCanceledException) { /* expected on shutdown */ }
        }
        _cts?.Dispose();
        _cts = null;
        _loop = null;
    }

    // Register a callback; dispose the returned token to unregister. Callbacks fire on the
    // dispatcher's background loop, so they should not block — sessions wrap the body in
    // app.Invoke to marshal the MessageBox back to the Terminal.Gui thread.
    public IDisposable Subscribe(Action<WallBroadcastDto> callback)
    {
        var id = Guid.NewGuid();
        _subscribers[id] = callback;
        return new Registration(this, id);
    }

    private async Task RunAsync(CancellationToken ct)
    {
        try
        {
            await foreach (var payload in _bus.SubscribeAsync(SystemTopics.Wall, ct).ConfigureAwait(false))
            {
                WallBroadcastDto? dto;
                try { dto = JsonSerializer.Deserialize<WallBroadcastDto>(payload); }
                catch (Exception ex)
                {
                    _logger.LogWarning(ex, "Wall payload failed to deserialize; dropping.");
                    continue;
                }
                if (dto is null) continue;

                // Iterating ConcurrentDictionary is safe under concurrent mutation — a
                // subscription added or removed mid-iteration may or may not see the
                // current broadcast, which is fine for at-most-once UX semantics.
                foreach (var kv in _subscribers)
                {
                    try { kv.Value(dto); }
                    catch (Exception ex)
                    {
                        // One bad subscriber must not kill the dispatch loop or starve
                        // its peers. Log and continue.
                        _logger.LogWarning(ex, "Wall subscriber callback threw; continuing dispatch.");
                    }
                }
            }
        }
        catch (OperationCanceledException) { /* expected on shutdown */ }
        catch (Exception ex)
        {
            _logger.LogError(ex, "WallDispatcher loop exited unexpectedly.");
        }
    }

    // IDisposable token tying a subscription to the registration site's lifetime. Using a
    // dedicated class (vs. an Action<Guid>) makes the `using var sub = dispatcher.Subscribe(...)`
    // pattern read cleanly at the call site and lets us guard against double-dispose.
    private sealed class Registration : IDisposable
    {
        private WallDispatcher? _owner;
        private readonly Guid _id;
        public Registration(WallDispatcher owner, Guid id) { _owner = owner; _id = id; }
        public void Dispose()
        {
            var owner = Interlocked.Exchange(ref _owner, null);
            owner?._subscribers.TryRemove(_id, out _);
        }
    }
}
