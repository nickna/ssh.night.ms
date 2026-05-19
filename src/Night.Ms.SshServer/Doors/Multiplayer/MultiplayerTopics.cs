namespace Night.Ms.SshServer.Doors.Multiplayer;

// Centralized topic + Redis key namer. Future doors get their own prefixes by passing a
// distinct gameKey; the framework never assumes "holdem" anywhere.
public static class MultiplayerTopics
{
    // Pub/sub: every seated session subscribes to Events; per-player private events (your
    // hole cards, action rejected) ride Private(user). Intents go on a Redis stream so the
    // coordinator can XREAD them in order even if it temporarily disconnects.
    public static string Events(string gameKey, long tableId) => $"mpdoor:{gameKey}:table:{tableId}:events";
    public static string Private(string gameKey, long tableId, long userId) => $"mpdoor:{gameKey}:table:{tableId}:private:{userId}";
    public static string IntentsStream(string gameKey, long tableId) => $"mpdoor:{gameKey}:table:{tableId}:intents";

    // Redis persistent keys (not pub/sub topics).
    public static string MetaKey(string gameKey, long tableId) => $"mpdoor:{gameKey}:table:{tableId}:meta";
    public static string SeatsKey(string gameKey, long tableId) => $"mpdoor:{gameKey}:table:{tableId}:seats";
    public static string StateKey(string gameKey, long tableId) => $"mpdoor:{gameKey}:table:{tableId}:state";
    public static string LeaseKey(string gameKey, long tableId) => $"mpdoor:{gameKey}:table:{tableId}:lock";
    public static string TablesSet(string gameKey) => $"mpdoor:{gameKey}:tables";

    // Reverse lookup so a reconnecting human can find their existing seat in O(1) without
    // scanning every table's seat hash. Coordinator maintains this on every sit-down /
    // stand-up / sit-out / abandonment-cleanup.
    public static string UserSeatKey(string gameKey, long userId) => $"mpdoor:{gameKey}:user:{userId}:seat";
}
