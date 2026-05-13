namespace Night.Ms.SshServer.Domain;

public sealed class Channel
{
    public long Id { get; set; }
    public required string Name { get; set; }
    public string? Topic { get; set; }
    public bool IsPrivate { get; set; }
    public long? CreatedById { get; set; }
    public DateTimeOffset CreatedAt { get; set; }

    public User? CreatedBy { get; set; }
    public List<ChannelMember> Members { get; set; } = [];
    public List<ChatMessage> Messages { get; set; } = [];
}

// Per-user "I've read messages up through ID X" pointer. Lets us count unread per channel
// cheaply (`COUNT(*) WHERE channel_id = ? AND id > ?`) and render a sidebar badge. UpdatedAt
// is the wall-clock time of the most recent bump, useful for sorting channels by recency.
public sealed class ChannelRead
{
    public long UserId { get; set; }
    public long ChannelId { get; set; }
    public long LastReadMessageId { get; set; }
    public DateTimeOffset UpdatedAt { get; set; }

    public User? User { get; set; }
    public Channel? Channel { get; set; }
}

public sealed class ChannelMember
{
    public long ChannelId { get; set; }
    public long UserId { get; set; }
    public DateTimeOffset JoinedAt { get; set; }
    public string Role { get; set; } = "member";

    public Channel? Channel { get; set; }
    public User? User { get; set; }
}

public sealed class ChatMessage
{
    public long Id { get; set; }
    public long ChannelId { get; set; }
    public long UserId { get; set; }
    public required string Body { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    // Set when the author edits a message. Display surfaces "(edited)" so other participants
    // can see the body was changed after the fact. Null means never edited.
    public DateTimeOffset? EditedAt { get; set; }
    // Tombstones are kept (not hard-deleted) so reactions and reply-context don't dangle and
    // moderation can audit what was said. The renderer paints "(deleted)" in faint gray.
    public DateTimeOffset? DeletedAt { get; set; }
    // Pinned messages render with a ★ prefix and are listed by `/pins`. Anyone can pin in
    // a public channel for now; private-channel-ops-only is a follow-up when ChannelMember
    // roles get enforced.
    public bool IsPinned { get; set; }
    // Threading: when set, this message is a reply to ParentMessageId. The reply appears
    // inline in the channel with a "↳" prefix, and the parent picks up a "[N replies]"
    // badge. We deliberately don't enforce a depth limit at the schema level — replies to
    // replies are stored flat (no chain reconstruction), so depth is always 1 in practice.
    public long? ParentMessageId { get; set; }

    public Channel? Channel { get; set; }
    public User? User { get; set; }
    public ChatMessage? ParentMessage { get; set; }
    public List<MessageReaction> Reactions { get; set; } = [];
}

// One row per (message, user, emoji) triplet. Reactions are simple toggles: same user can
// add multiple distinct emoji to the same message but adding the same emoji twice is a
// no-op (enforced by the composite PK). The Emoji field is the rendered unicode glyph, not
// the shortcode — that way clients don't need an emoji table to render historical data.
public sealed class MessageReaction
{
    public long MessageId { get; set; }
    public long UserId { get; set; }
    public required string Emoji { get; set; }
    public DateTimeOffset CreatedAt { get; set; }

    public ChatMessage? Message { get; set; }
    public User? User { get; set; }
}
