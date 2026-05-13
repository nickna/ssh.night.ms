namespace Night.Ms.SshServer.Tui.Chat;

// Mutable snapshot of an on-screen message. Position-based commands (/edit 3, /react 2, …)
// resolve against the screen's ordered list of these, and envelope handlers (edit, pin,
// delete) update them in place so the screen can re-render without swapping list entries.
//
// Shared by ChatScreen and ChatThreadScreen. The thread screen leaves ParentMessageId null
// and ReplyCount 0 — the channel screen uses both to render the "↳ @parent" prefix and the
// "[N replies]" badge.
internal sealed class MessageRef
{
    public required long MessageId { get; init; }
    public required string Handle { get; init; }
    public required DateTimeOffset At { get; init; }
    public required string Body { get; set; }
    public bool Edited { get; set; }
    public bool Pinned { get; set; }
    public bool Deleted { get; set; }
    public long? ParentMessageId { get; init; }
    public int ReplyCount { get; set; }
}
