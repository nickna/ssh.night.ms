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

    public async IAsyncEnumerable<byte[]> SubscribeAsync(
        string topic,
        [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        var channel = RedisChannel.Literal(topic);
        var queue = Channel.CreateUnbounded<byte[]>(new UnboundedChannelOptions
        {
            SingleReader = true,
            SingleWriter = false,
        });

        void OnMessage(RedisChannel _, RedisValue value)
        {
            // Redis delivers on a thread-pool callback; just drop into the bounded queue
            // and let the consumer coroutine drain it on its own scheduling.
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
