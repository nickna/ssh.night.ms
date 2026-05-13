using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Realtime;

// Role decisions for chat mutations live here so each mutation isn't reimplementing
// "you only own this message" or "only the channel creator may X" with a slightly different
// fallback rule. Sysops bypass all per-message and per-channel checks — the moderation
// surface relies on this so a sysop can clean up a bad message without shelling in.
internal static class ChatAuthorization
{
    // Edit/Delete/Pin/Unpin: actor must own the message, unless they're sysop. Deleted
    // messages can still be unmoderated by their owner (re-delete is a no-op there) but
    // the caller is expected to short-circuit on the DeletedAt check separately.
    public static bool CanModifyMessage(ChatMessage msg, long actorUserId, bool actorIsSysop) =>
        actorIsSysop || msg.UserId == actorUserId;

    // Pin/Unpin: any participant today. Sysop is implicitly allowed (true falls through).
    // Tightening to channel-ops or creator-only is a one-line change here without touching
    // each mutation method.
    public static bool CanPinInChannel(Channel _, long _actorUserId, bool _actorIsSysop) => true;

    // SetTopic: channel creator only. Admin-seeded rows (CreatedById is null) fall through
    // to any-member, matching the pre-existing behavior. Sysops always pass.
    public static bool CanSetChannelTopic(Channel channel, long actorUserId, bool actorIsSysop)
    {
        if (actorIsSysop) return true;
        return channel.CreatedById is not { } creatorId || creatorId == actorUserId;
    }
}
