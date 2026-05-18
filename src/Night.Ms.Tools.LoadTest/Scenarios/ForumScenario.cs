using System.Diagnostics;
using Night.Ms.Tools.LoadTest.Bots;
using Night.Ms.Tools.LoadTest.Metrics;

namespace Night.Ms.Tools.LoadTest.Scenarios;

// Drives a bot through the forum write path:
//   lobby → 'B' → ForumList → Enter on first row (General, seeded by DatabaseInitializer)
//        → TopicList → 'N' → NewTopic → type title + Tab + body → Ctrl+S
//        → wait for the new topic to appear in the topic list
// Then back to lobby and repeat. The unique "lt-{botIndex}-{seq}-{utcMs}" title means
// the topic-visible check can't false-positive against pre-existing rows.
public sealed class ForumScenario : IScenario
{
    private const byte CtrlS = 0x13;
    private const byte Tab = 0x09;

    private readonly int _botIndex;

    public ForumScenario(int botIndex)
    {
        _botIndex = botIndex;
    }

    public string Name => "forum";

    public async Task RunAsync(Bot bot, MetricsCollector metrics, CancellationToken ct)
    {
        if (!await bot.Screen.WaitForAsync("Welcome", TimeSpan.FromSeconds(20), ct).ConfigureAwait(false))
        {
            metrics.IncrementError("lobby.land");
            return;
        }

        var rng = new Random(_botIndex * 7927);
        var seq = 0;
        while (!ct.IsCancellationRequested)
        {
            seq++;
            await OnePassAsync(bot, metrics, seq, ct).ConfigureAwait(false);

            // Pause between passes — at ~30-90s per pass, with the new-topic + back-to-lobby
            // sequence, we naturally pace at ~1 post per minute per bot. Don't compound that
            // with a long sleep here, but a small jitter keeps bots from synchronising.
            try { await Task.Delay(5000 + rng.Next(0, 5000), ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
        }
    }

    private async Task OnePassAsync(Bot bot, MetricsCollector metrics, int seq, CancellationToken ct)
    {
        // Step 1: lobby → ForumList
        await bot.SendKeyAsync('B').ConfigureAwait(false);
        if (!await bot.Screen.WaitForAsync("boards", TimeSpan.FromSeconds(5), ct).ConfigureAwait(false))
        {
            metrics.IncrementError("forum.enter_list");
            return;
        }

        // Step 2: ForumList → TopicList (Enter selects the first forum). On a fresh DB
        // that's "General"; any other configuration is operator-controlled and the title
        // banner check below will still match because we look only for "boards/".
        await bot.SendEnterAsync().ConfigureAwait(false);
        if (!await bot.Screen.WaitForAsync("[N]ew topic", TimeSpan.FromSeconds(5), ct).ConfigureAwait(false))
        {
            metrics.IncrementError("forum.enter_topics");
            await EscapeToLobbyAsync(bot, ct).ConfigureAwait(false);
            return;
        }

        // Step 3: TopicList → NewTopicScreen
        await bot.SendKeyAsync('N').ConfigureAwait(false);
        if (!await bot.Screen.WaitForAsync("new topic", TimeSpan.FromSeconds(5), ct).ConfigureAwait(false))
        {
            metrics.IncrementError("forum.enter_new");
            await EscapeToLobbyAsync(bot, ct).ConfigureAwait(false);
            return;
        }

        // Step 4: type title, Tab to body, type body, Ctrl+S
        var title = $"lt-{_botIndex:D4}-{seq}-{DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()}";
        await bot.SendAsync(title).ConfigureAwait(false);
        await bot.SendAsync(new string((char)Tab, 1)).ConfigureAwait(false);
        await bot.SendAsync($"body for {title} — load test fill content").ConfigureAwait(false);

        var sw = Stopwatch.StartNew();
        await bot.SendAsync(new string((char)CtrlS, 1)).ConfigureAwait(false);

        // Step 5: wait for the unique title to show up in the topic list. 10 s is a
        // generous ceiling — anything past that gets counted as a failure.
        var landed = await bot.Screen.WaitForAsync(title, TimeSpan.FromSeconds(10), ct).ConfigureAwait(false);
        sw.Stop();
        if (landed) metrics.Record("forum.new_topic_ms", sw.Elapsed);
        else metrics.IncrementError("forum.new_topic");

        // Step 6: back to lobby for the next pass.
        await EscapeToLobbyAsync(bot, ct).ConfigureAwait(false);
    }

    // Hammers Esc up to three times: NewTopic / Thread → TopicList → ForumList → Lobby.
    // Each Esc completes a single screen-pop in the lobby loop; if we're already on the
    // lobby the extra presses are harmless (lobby ignores stray Esc).
    private static async Task EscapeToLobbyAsync(Bot bot, CancellationToken ct)
    {
        for (var i = 0; i < 3; i++)
        {
            await bot.SendEscAsync().ConfigureAwait(false);
            try { await Task.Delay(150, ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
        }
        // Wait briefly for the lobby to repaint, but don't fail the scenario if it doesn't
        // — the next pass's "boards" check will catch a stuck navigation.
        await bot.Screen.WaitForAsync("Welcome", TimeSpan.FromSeconds(5), ct).ConfigureAwait(false);
    }
}
