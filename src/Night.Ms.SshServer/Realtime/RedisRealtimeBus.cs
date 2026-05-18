using System.Runtime.CompilerServices;
using System.Threading.Channels;
using StackExchange.Redis;

namespace Night.Ms.SshServer.Realtime;

public sealed class RedisRealtimeBus(IConnectionMultiplexer redis, ILogger<RedisRealtimeBus> logger) : IRealtimeBus
{
    private readonly ISubscriber _subscriber = redis.GetSubscriber();

    public Task PublishAsync(string topic, byte[] payload, CancellationToken cancellationToken = default)
    {
        cancellationToken.ThrowIfCancellationRequested();
        return _subscriber.PublishAsync(RedisChannel.Literal(topic), payload);
    }

    // Bounded buffer per subscription so a stuck consumer (e.g., a hung UI thread) can't
    // balloon process memory while Redis keeps publishing. 256 messages is generous for
    // chat-style envelopes (low-KB each); when full we drop the oldest, which matches the
    // existing best-effort semantics — chat is allowed to skip old events under back-pressure
    // rather than queue indefinitely or break the publisher.
    private const int SubscriptionBufferCapacity = 256;

    public async IAsyncEnumerable<byte[]> SubscribeAsync(
        string topic,
        [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        var channel = RedisChannel.Literal(topic);
        var queue = Channel.CreateBounded<byte[]>(new BoundedChannelOptions(SubscriptionBufferCapacity)
        {
            SingleReader = true,
            SingleWriter = false,
            FullMode = BoundedChannelFullMode.DropOldest,
        });

        void OnMessage(RedisChannel _, RedisValue value)
        {
            // Redis delivers on a thread-pool callback; drop into the bounded queue and
            // let the consumer coroutine drain it on its own scheduling. TryWrite never
            // blocks here — DropOldest evicts the head if the buffer is full.
            if (value.HasValue)
            {
                queue.Writer.TryWrite((byte[])value!);
            }
        }

        await _subscriber.SubscribeAsync(channel, OnMessage).ConfigureAwait(false);
        try
        {
            while (await queue.Reader.WaitToReadAsync(cancellationToken).ConfigureAwait(false))
            {
                while (queue.Reader.TryRead(out var payload))
                {
                    yield return payload;
                }
            }
        }
        finally
        {
            try
            {
                await _subscriber.UnsubscribeAsync(channel, OnMessage).ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                logger.LogDebug(ex, "Unsubscribe from {Topic} failed (non-fatal)", topic);
            }
        }
    }
}
