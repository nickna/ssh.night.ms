namespace Night.Ms.SshServer.Realtime;

// Thin pub/sub abstraction so chat code doesn't bind to StackExchange.Redis directly. M10
// polish can plug in an in-process implementation for tests and a no-op for degraded mode.
public interface IRealtimeBus
{
    Task PublishAsync(string topic, byte[] payload, CancellationToken cancellationToken = default);

    // The yielded sequence runs until the cancellation token fires or the subscription drops.
    // Implementations are responsible for marshaling delivery to a thread-safe buffer; consumers
    // can iterate without worrying about reentrancy.
    IAsyncEnumerable<byte[]> SubscribeAsync(string topic, CancellationToken cancellationToken = default);
}
