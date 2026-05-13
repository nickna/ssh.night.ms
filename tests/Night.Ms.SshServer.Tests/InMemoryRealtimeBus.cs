using System.Threading.Channels;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tests;

// Test double for IRealtimeBus — captures published payloads in-process so a test can
// assert on what would have hit Redis without spinning up a real container. Subscribe
// returns an empty stream (presence/edits/etc. don't fan back into the publisher
// for the tests that use this), which matches the production semantics: a single
// session never re-receives its own publishes via the wire — the local screen renders
// them directly off the send path.
public sealed class InMemoryRealtimeBus : IRealtimeBus
{
    private readonly List<(string Topic, byte[] Payload)> _published = new();

    public IReadOnlyList<(string Topic, byte[] Payload)> Published => _published;

    public Task PublishAsync(string topic, byte[] payload, CancellationToken cancellationToken = default)
    {
        _published.Add((topic, payload));
        return Task.CompletedTask;
    }

    public async IAsyncEnumerable<byte[]> SubscribeAsync(
        string topic,
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        // Block on a never-completing channel so the iterator stays open until the test
        // cancels the token. Mirrors RedisRealtimeBus' "yields until cancellation" shape.
        var queue = Channel.CreateUnbounded<byte[]>();
        try
        {
            while (await queue.Reader.WaitToReadAsync(cancellationToken))
            {
                while (queue.Reader.TryRead(out var payload))
                {
                    yield return payload;
                }
            }
        }
        finally
        {
            queue.Writer.TryComplete();
        }
    }
}
