using System.Text.Json;

namespace Night.Ms.SshServer.Realtime;

public static class ChatTopics
{
    public static string Channel(long channelId) => $"chat:channel:{channelId}";

    // Presence is a separate topic so a session can subscribe to "who's around" without
    // ferrying every chat message through the presence handler. One topic per channel.
    public static string Presence(long channelId) => $"chat:presence:{channelId}";
}

// Discriminator over the per-channel topic. One JSON envelope per published event lets us
// keep a single Redis subscription per channel and dispatch on Kind at the consumer. New
// kinds (typing indicators, pins, etc.) can be added without changing the topic shape.
public static class ChatEventKind
{
    public const string Message  = "msg";
    public const string Edit     = "edit";
    public const string Delete   = "del";
    public const string React    = "react";
    public const string Unreact  = "unreact";
    public const string Pin      = "pin";
    public const string Unpin    = "unpin";
    public const string Topic    = "topic";
}

public sealed record ChatEnvelope(string Kind, JsonElement Payload);

// ParentMessageId is null for top-level messages, set to the parent's id for /reply. We
// keep it on the on-the-wire DTO (not just the DB row) so receiving sessions can render
// the "↳ in reply to" prefix without an extra DB round-trip.
public sealed record ChatMessageDto(
    long Id,
    long ChannelId,
    long UserId,
    string Handle,
    string Body,
    DateTimeOffset CreatedAt,
    long? ParentMessageId = null);

// Sent when an author edits an existing message. Body is the new value; clients update the
// in-memory display in place. Pre-edit body is not transmitted — the audit trail (if any)
// lives in the DB, not on the wire.
public sealed record ChatEditDto(long MessageId, long ChannelId, string Body, DateTimeOffset EditedAt);

// Sent when an author (or sysop) deletes a message. The DB row is tombstoned (DeletedAt set),
// not hard-deleted, so reactions and surrounding context don't dangle.
public sealed record ChatDeleteDto(long MessageId, long ChannelId);

// Sent when a user adds or removes an emoji reaction. UserId is the reactor; Emoji is the
// unicode glyph (already substituted), not the shortcode.
public sealed record ChatReactionDto(long MessageId, long ChannelId, long UserId, string Handle, string Emoji);

// Sent when a message is pinned or unpinned. Other sessions re-render the entry with the
// pinned marker on/off without re-querying the DB.
public sealed record ChatPinDto(long MessageId, long ChannelId, bool IsPinned);

// Sent when a channel topic is set. Other sessions update their chrome immediately.
public sealed record ChatTopicDto(long ChannelId, string? Topic, long ActorUserId, string ActorHandle);

// Presence events on the dedicated chat:presence:{channelId} topic. "join" and "leave" are
// edges; "heartbeat" is the periodic refresh that keeps a session alive in the Redis TTL set.
public sealed record PresenceEventDto(long ChannelId, long UserId, string Handle, string Kind, DateTimeOffset At);

public static class PresenceEventKind
{
    public const string Join      = "join";
    public const string Leave     = "leave";
    public const string Heartbeat = "heartbeat";
    // Transient — sent on keystroke (debounced), not persisted anywhere. Listeners render
    // "alice is typing…" and auto-clear after a few seconds of silence.
    public const string Typing    = "typing";
}
