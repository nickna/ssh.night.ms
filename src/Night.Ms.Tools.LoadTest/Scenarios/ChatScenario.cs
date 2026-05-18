using System.Diagnostics;
using System.Text.RegularExpressions;
using Night.Ms.Tools.LoadTest.Bots;
using Night.Ms.Tools.LoadTest.Metrics;

namespace Night.Ms.Tools.LoadTest.Scenarios;

// Drives a bot through the headline chat path:
//   land on lobby → press 'C' to enter chat → wait for the chat UI marker →
//   send timestamped "mlt-{botId}-{seq}-{utcMs}" markers on a randomised cadence,
//   concurrently scan ScreenBuffer for *other* bots' markers and record the
//   publish-to-receive latency, plus periodic /finger interjections to exercise
//   the profile path.
//
// "mlt-" prefix is deliberately distinctive: rare enough not to false-positive
// against TG chrome or other carousel labels, short enough that it round-trips
// even on the slowest possible render.
public sealed class ChatScenario : IScenario
{
    private static readonly Regex MarkerRegex = new(
        @"mlt-(\d{4})-(\d+)-(\d+)",
        RegexOptions.Compiled);

    // Gate so we only dump one bot's stuck-on-lobby screen even at N=500.
    private static int s_chatEnterDumpsEmitted;

    private readonly int _botIndex;

    public ChatScenario(int botIndex)
    {
        _botIndex = botIndex;
    }

    public string Name => "chat";

    public async Task RunAsync(Bot bot, MetricsCollector metrics, CancellationToken ct)
    {
        // Land on lobby first — connect already happened in Driver.RunBotAsync.
        if (!await bot.Screen.WaitForAsync("Welcome", TimeSpan.FromSeconds(20), ct).ConfigureAwait(false))
        {
            metrics.IncrementError("lobby.land");
            return;
        }
        metrics.Record("lobby.land_ms", TimeSpan.Zero); // presence-only marker; the real timing is captured by the IdleScenario sample if mixed.

        // Brief settle: 'Welcome' is painted on first frame but the carousel's hotkey
        // handlers can take an extra render tick to attach. Sending too early causes
        // the keystroke to be swallowed before the binding lands.
        try { await Task.Delay(500, ct).ConfigureAwait(false); }
        catch (OperationCanceledException) { return; }

        // Lowercase: TG v2's hotkey comparison maps 'c' (0x63) to Key.C; uppercase 'C'
        // (0x43) is interpreted as Shift+C and may not match a hotkey defined as Key.C.
        await bot.SendKeyAsync('c').ConfigureAwait(false);

        // Chat-screen title contains "#lobby". The lobby's own title says "lobby" without
        // the # so it can't false-positive against the lobby screen we just left.
        if (!await bot.Screen.WaitForAsync("#lobby", TimeSpan.FromSeconds(10), ct).ConfigureAwait(false))
        {
            metrics.IncrementError("chat.enter");
            // Dump up to the last 600 chars of the ANSI-stripped screen — once per run —
            // so the next run tells us what was actually rendered when the timeout hit.
            if (Interlocked.Increment(ref s_chatEnterDumpsEmitted) == 1)
            {
                var snap = bot.Screen.StrippedSnapshot();
                if (snap.Length > 600) snap = snap.Substring(snap.Length - 600);
                Console.Error.WriteLine($"loadtest: bot {bot.Handle} chat.enter timeout; tail of screen:\n{snap}\n---");
            }
            return;
        }

        // Each bot tracks its own random number generator to avoid contention; seeded
        // by index so a re-run with the same N produces the same cadence pattern,
        // useful for diffing runs.
        var rng = new Random(_botIndex * 7919);

        // Local cache of (sender, seq) pairs we've already recorded — without it,
        // every poll of a 32 KB buffer would re-credit the same message.
        var seen = new HashSet<string>(StringComparer.Ordinal);

        using var localCts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        var receiveTask = ReceiveLoopAsync(bot, metrics, seen, localCts.Token);
        var sendTask = SendLoopAsync(bot, rng, localCts.Token);
        var fingerTask = FingerLoopAsync(bot, metrics, rng, localCts.Token);

        try
        {
            await Task.WhenAll(receiveTask, sendTask, fingerTask).ConfigureAwait(false);
        }
        catch (OperationCanceledException) { /* expected on shutdown */ }
    }

    private async Task SendLoopAsync(Bot bot, Random rng, CancellationToken ct)
    {
        var seq = 0;
        while (!ct.IsCancellationRequested)
        {
            // 2-6 second cadence. Faster than that and the test resembles a synthetic
            // benchmark more than realistic chat; slower means we don't accumulate
            // enough latency samples for stable percentiles.
            var waitMs = 2000 + rng.Next(0, 4000);
            try { await Task.Delay(waitMs, ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }

            seq++;
            var marker = $"mlt-{_botIndex:D4}-{seq}-{DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()}";
            try
            {
                await bot.SendAsync(marker).ConfigureAwait(false);
                await bot.SendEnterAsync().ConfigureAwait(false);
            }
            catch
            {
                // Drop on the floor — if the transport's broken, the connect/receive
                // sides will surface their own errors. We don't want to double-count.
                return;
            }
        }
    }

    private async Task ReceiveLoopAsync(Bot bot, MetricsCollector metrics, HashSet<string> seen, CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try { await Task.Delay(100, ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }

            var stripped = bot.Screen.StrippedSnapshot();
            var now = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            foreach (Match m in MarkerRegex.Matches(stripped))
            {
                var sender = m.Groups[1].Value;
                var seq = m.Groups[2].Value;
                if (sender == $"{_botIndex:D4}") continue; // skip own echoes
                var key = $"{sender}-{seq}";
                if (!seen.Add(key)) continue; // already counted
                if (!long.TryParse(m.Groups[3].Value, out var utcMs)) continue;
                var elapsed = TimeSpan.FromMilliseconds(Math.Max(0, now - utcMs));
                metrics.Record("chat.publish_to_receive_ms", elapsed);
            }
        }
    }

    private async Task FingerLoopAsync(Bot bot, MetricsCollector metrics, Random rng, CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            // ~20s cadence with jitter so all bots aren't fingering simultaneously.
            var waitMs = 15000 + rng.Next(0, 10000);
            try { await Task.Delay(waitMs, ct).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }

            // Pick a random other bot. Bot count comes from the index space implicitly;
            // we randomise from the same range we know was seeded. Realistic enough.
            var target = rng.Next(1, Math.Max(2, _botIndex * 2));
            var sw = Stopwatch.StartNew();
            try
            {
                await bot.SendAsync($"/finger loadbot-{target:D4}").ConfigureAwait(false);
                await bot.SendEnterAsync().ConfigureAwait(false);
            }
            catch { return; }

            // Finger renders "── finger loadbot-NNNN ──" inline in the chat log.
            // Wait up to 5s; longer than that is a regression, count as error.
            var matched = await bot.Screen.WaitForAsync($"── finger loadbot-{target:D4}", TimeSpan.FromSeconds(5), ct).ConfigureAwait(false);
            sw.Stop();
            if (matched) metrics.Record("chat.finger_ms", sw.Elapsed);
            else metrics.IncrementError("chat.finger");
        }
    }
}
